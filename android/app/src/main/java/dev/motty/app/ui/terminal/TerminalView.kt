package dev.motty.app.ui.terminal

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Typeface
import android.text.InputType
import android.util.AttributeSet
import android.util.TypedValue
import android.view.Choreographer
import android.view.HapticFeedbackConstants
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.VelocityTracker
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager
import dev.motty.app.core.CoreRepository
import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.math.abs
import kotlin.math.ceil
import kotlin.math.exp
import kotlin.math.floor
import kotlin.math.roundToLong

/**
 * Draws the cell grid the Go terminal produces and sends input back. Like the
 * iOS view, it keeps no terminal state of its own: every frame asks the core
 * for a snapshot and paints it. Scrolling is pixel-accurate under the finger;
 * the core only stores whole-line offsets, so the leftover fraction is drawn
 * as a translation.
 */
class TerminalView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : View(context, attrs) {

    var repository: CoreRepository? = null
        private set
    var sessionID: Long = 0
        private set

    /** Fires when modes that affect input encoding change. */
    var onModesChanged: ((appCursorKeys: Boolean, bracketedPaste: Boolean) -> Unit)? = null

    /**
     * The system "monospace" family is not monospaced on every OEM build (a
     * ColorOS device resolved it to a proportional face, which put every cell
     * on the wrong column). Prefer a real monospace font file when one is
     * present, and verify it before trusting it.
     */
    private val terminalTypeface: Typeface =
        loadMonospaceTypeface() ?: Typeface.MONOSPACE

    private val textPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        typeface = terminalTypeface
        // Terminal cells are a fixed size, like the iOS view's 13 pt: the
        // system font scale must not change how many columns fit.
        textSize = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            13f,
            resources.displayMetrics,
        )
    }
    private val backgroundPaint = Paint()
    private val cursorPaint = Paint(Paint.ANTI_ALIAS_FLAG)
    private val selectionPaint = Paint()
    private val preeditPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        typeface = terminalTypeface
        textSize = this@TerminalView.textPaint.textSize
        color = 0xFF32D99F.toInt()
        isUnderlineText = true
    }

    private val cellWidth: Float
    private val cellHeight: Float
    private val textBaselineShift: Float

    private val background = 0xFF0D0E13.toInt()
    private val foreground = 0xFFE6E8EC.toInt()
    private val selectionColor = 0x553B82F6

    private val clipboard = context.getSystemService(ClipboardManager::class.java)
    private val inputMethodManager = context.getSystemService(InputMethodManager::class.java)

    private var buffer = ByteBuffer.allocateDirect(512 * 1024).order(ByteOrder.LITTLE_ENDIAN)
    private var lastVersion = 0L

    private var cols = 0
    private var rows = 0
    private var cursorX = 0
    private var cursorY = 0
    private var cursorStyle = 0
    private var cursorVisible = false
    private var flags = 0
    private var scrollback = 0
    private var viewOffset = 0

    private var cellRune = IntArray(0)
    private var cellFg = IntArray(0)
    private var cellBg = IntArray(0)
    private var cellAttr = IntArray(0)
    private var cellWidths = ByteArray(0)
    private var cellFlags = ByteArray(0)
    private var rowCounts = IntArray(0)

    private var appCursorKeys = false
    private var bracketedPaste = false
    private var altScreen = false

    private var scrollActive = false
    private var dragging = false
    private var scrollBaseOffset = 0f
    private var scrollDistancePx = 0f
    private var scrollVelocity = 0f
    private var requestedOffset = 0
    private var settleTarget: Int? = null
    private var settleFrames = 0
    private var lastVisualOffset = 0f

    private var selectionAnchor: CellPos? = null
    private var selectionFocus: CellPos? = null
    private var mode = DragMode.NONE
    private var downX = 0f
    private var downY = 0f
    private var lastY = 0f
    private var velocityTracker: VelocityTracker? = null

    private var composing = ""
    private var lastFrameNanos = 0L

    private var pendingCols = 0
    private var pendingRows = 0
    private var requestedCols = 0
    private var requestedRows = 0

    private val touchSlop = ViewConfiguration.get(context).scaledTouchSlop
    private val resizeRunnable = Runnable { commitResize() }
    private val longPressRunnable = Runnable {
        if (mode == DragMode.NONE) {
            startSelection(downX, downY)
        }
    }

    private var frameCallback: Choreographer.FrameCallback? = null

    private enum class DragMode { NONE, SCROLL, SELECT }

    private data class CellPos(val row: Int, val col: Int)

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        setBackgroundColor(background)
        val metrics = textPaint.fontMetrics
        cellWidth = textPaint.measureText("M").takeIf { it > 0f } ?: (textPaint.textSize * 0.6f)
        cellHeight = ceil(metrics.descent - metrics.ascent).coerceAtLeast(1f) + 2f
        textBaselineShift = (cellHeight - (metrics.descent - metrics.ascent)) / 2f - metrics.ascent
        cursorPaint.color = (foreground and 0x00FFFFFF) or 0x8C000000.toInt()
        selectionPaint.color = selectionColor
    }

    fun bind(repository: CoreRepository, sessionID: Long) {
        this.repository = repository
        this.sessionID = sessionID
        lastVersion = 0
        lastVisualOffset = 0f
        scrollActive = false
        settleTarget = null
        selectionAnchor = null
        selectionFocus = null
        requestedCols = 0
        requestedRows = 0
        if (pendingCols > 0) {
            removeCallbacks(resizeRunnable)
            postDelayed(resizeRunnable, 120)
        }
        invalidate()
    }

    fun send(bytes: ByteArray) {
        if (bytes.isEmpty()) return
        repository?.queueInput(sessionID, bytes)
    }

    fun focusInput() {
        requestFocus()
        val imm = inputMethodManager ?: return
        imm.showSoftInput(this, InputMethodManager.SHOW_IMPLICIT)
    }

    fun hasSelection(): Boolean = selectionRange() != null

    /** Copies the selection, or the visible screen when nothing is selected. */
    fun copyScreen(): Boolean {
        val selection = selectedText()
        val text = selection ?: visibleText()
        if (text.isEmpty()) return false
        clipboard?.setPrimaryClip(ClipData.newPlainText("Motty", text))
        if (selection != null) {
            selectionAnchor = null
            selectionFocus = null
            invalidate()
        }
        return true
    }

    fun pasteClipboard(): Boolean {
        val clip = clipboard?.primaryClip ?: return false
        if (clip.itemCount == 0) return false
        val text = clip.getItemAt(0).coerceToText(context)?.toString() ?: return false
        if (text.isEmpty()) return false
        val capped = text.take(MAX_PASTE_CHARS)
        val payload = if (bracketedPaste) "\u001B[200~$capped\u001B[201~" else capped
        send(payload.toByteArray(Charsets.UTF_8))
        return true
    }

    // ---- lifecycle ----

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        startFrames()
    }

    override fun onDetachedFromWindow() {
        stopFrames()
        removeCallbacks(resizeRunnable)
        removeCallbacks(longPressRunnable)
        velocityTracker?.recycle()
        velocityTracker = null
        super.onDetachedFromWindow()
    }

    private fun startFrames() {
        if (frameCallback != null) return
        val callback = object : Choreographer.FrameCallback {
            override fun doFrame(frameTimeNanos: Long) {
                tick(frameTimeNanos)
                if (frameCallback != null) {
                    Choreographer.getInstance().postFrameCallback(this)
                }
            }
        }
        frameCallback = callback
        Choreographer.getInstance().postFrameCallback(callback)
    }

    private fun stopFrames() {
        frameCallback?.let { Choreographer.getInstance().removeFrameCallback(it) }
        frameCallback = null
    }

    private fun tick(frameTimeNanos: Long) {
        val delta = if (lastFrameNanos == 0L) {
            1f / 60f
        } else {
            ((frameTimeNanos - lastFrameNanos) / 1_000_000_000.0).toFloat().coerceIn(0f, 0.05f)
        }
        lastFrameNanos = frameTimeNanos

        advanceFling(delta)
        if (scrollActive) commitScrollTarget()

        var changed = fetchSnapshot()
        if (settleTarget != null) {
            settleFrames++
            if (viewOffset == settleTarget || settleFrames > 30) {
                finishScroll()
                changed = true
            }
        }
        val visual = visualOffset()
        if (changed || abs(visual - lastVisualOffset) > 0.01f) {
            lastVisualOffset = visual
            invalidate()
        }
    }

    // ---- snapshot ----

    private fun fetchSnapshot(): Boolean {
        val repo = repository ?: return false
        val count = try {
            repo.snapshot(sessionID, lastVersion, buffer)
        } catch (_: Throwable) {
            return false
        }
        if (count > 0) {
            if (!parse(count)) return false
            lastVersion = snapshotVersion
            return true
        }
        if (count < 0) {
            val needed = -count
            if (needed in 1..MAX_SNAPSHOT_BYTES && needed > buffer.capacity()) {
                buffer = ByteBuffer
                    .allocateDirect(maxOf(needed, buffer.capacity() * 2))
                    .order(ByteOrder.LITTLE_ENDIAN)
            }
        }
        return false
    }

    private var snapshotVersion = 0L

    private fun parse(length: Int): Boolean {
        if (length < HEADER_SIZE) return false
        val data = buffer
        snapshotVersion = data.getLong(0)
        val newCols = data.getInt(8)
        val newRows = data.getInt(12)
        if (newCols <= 0 || newRows <= 0 || newCols > 1000 || newRows > 1000) return false
        val newCursorX = data.getInt(16)
        val newCursorY = data.getInt(20)
        val newCursorStyle = data.getInt(24)
        val newFlags = data.getInt(28)
        val newScrollback = data.getInt(32)
        val newViewOffset = data.getInt(36)

        val size = newCols * newRows
        if (cellRune.size < size) {
            cellRune = IntArray(size)
            cellFg = IntArray(size)
            cellBg = IntArray(size)
            cellAttr = IntArray(size)
            cellWidths = ByteArray(size)
            cellFlags = ByteArray(size)
        }
        if (rowCounts.size < newRows) rowCounts = IntArray(newRows)

        var offset = HEADER_SIZE
        for (y in 0 until newRows) {
            if (offset + 4 > length) return false
            val count = data.getInt(offset)
            offset += 4
            if (count < 0 || count > newCols) return false
            if (offset + count * CELL_SIZE > length) return false
            rowCounts[y] = count
            val base = y * newCols
            for (x in 0 until count) {
                val cell = offset + x * CELL_SIZE
                val index = base + x
                cellRune[index] = data.getInt(cell)
                cellFg[index] = data.getInt(cell + 4)
                cellBg[index] = data.getInt(cell + 8)
                cellAttr[index] = data.getShort(cell + 12).toInt() and 0xFFFF
                cellWidths[index] = data.get(cell + 14)
                cellFlags[index] = data.get(cell + 15)
            }
            offset += count * CELL_SIZE
        }
        cols = newCols
        rows = newRows
        cursorX = newCursorX
        cursorY = newCursorY
        cursorStyle = newCursorStyle
        flags = newFlags
        scrollback = newScrollback
        viewOffset = newViewOffset
        cursorVisible = (flags and FLAG_CURSOR_VISIBLE) != 0

        val newAlt = (flags and FLAG_ALT_SCREEN) != 0
        val newAppCursor = (flags and FLAG_APP_CURSOR_KEYS) != 0
        val newBracketed = (flags and FLAG_BRACKETED_PASTE) != 0
        val modesChanged = newAppCursor != appCursorKeys || newBracketed != bracketedPaste
        altScreen = newAlt
        appCursorKeys = newAppCursor
        bracketedPaste = newBracketed
        if (modesChanged) onModesChanged?.invoke(appCursorKeys, bracketedPaste)
        if (altScreen && scrollActive) finishScroll()
        return true
    }

    // ---- sizing ----

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        val newCols = maxOf(1, (w / cellWidth).toInt())
        val newRows = maxOf(1, (h / cellHeight).toInt())
        if (newCols == pendingCols && newRows == pendingRows) return
        pendingCols = newCols
        pendingRows = newRows
        removeCallbacks(resizeRunnable)
        postDelayed(resizeRunnable, 120)
    }

    private fun commitResize() {
        if (sessionID == 0L || pendingCols <= 0 || pendingRows <= 0) return
        if (pendingCols == requestedCols && pendingRows == requestedRows) return
        requestedCols = pendingCols
        requestedRows = pendingRows
        repository?.requestResize(sessionID, pendingCols, pendingRows)
        invalidate()
    }

    // ---- scrolling ----

    private fun beginScroll() {
        scrollBaseOffset = if (scrollActive) rawScrollOffset() else viewOffset.toFloat()
        scrollDistancePx = 0f
        scrollVelocity = 0f
        settleTarget = null
        settleFrames = 0
        requestedOffset = viewOffset
        scrollActive = true
    }

    private fun rawScrollOffset(): Float = scrollBaseOffset + scrollDistancePx / cellHeight

    private fun clampedScrollOffset(): Float =
        rawScrollOffset().coerceIn(0f, maxOf(0, scrollback).toFloat())

    private fun commitScrollTarget() {
        if (!scrollActive) return
        val target = settleTarget ?: floor(clampedScrollOffset()).toInt()
        val bounded = target.coerceIn(0, maxOf(0, scrollback))
        if (bounded == requestedOffset) return
        requestedOffset = bounded
        repository?.requestScrollTo(sessionID, bounded)
    }

    private fun visualOffset(): Float {
        if (!scrollActive) return 0f
        val difference = (clampedScrollOffset() - viewOffset) * cellHeight
        val limit = cellHeight * 6
        return difference.coerceIn(-limit, limit)
    }

    private fun settleScroll() {
        scrollVelocity = 0f
        val target = rawScrollOffset().roundToLong().toInt()
            .coerceIn(0, maxOf(0, scrollback))
        scrollBaseOffset = target.toFloat()
        scrollDistancePx = 0f
        settleTarget = target
        settleFrames = 0
        if (viewOffset == target) {
            finishScroll()
        }
    }

    private fun finishScroll() {
        scrollActive = false
        dragging = false
        scrollVelocity = 0f
        settleTarget = null
        settleFrames = 0
        scrollBaseOffset = 0f
        scrollDistancePx = 0f
        invalidate()
    }

    private fun advanceFling(delta: Float) {
        if (!scrollActive || dragging || scrollVelocity == 0f) return
        val maximum = maxOf(0, scrollback).toFloat()
        val next = rawScrollOffset() + scrollVelocity * delta / cellHeight
        if (next < 0f || next > maximum) {
            scrollDistancePx = ((next.coerceIn(0f, maximum)) - scrollBaseOffset) * cellHeight
            settleScroll()
            return
        }
        scrollDistancePx += scrollVelocity * delta
        scrollVelocity *= exp(-delta / 0.32f)
        if (abs(scrollVelocity) < 40f) {
            settleScroll()
        }
    }

    // ---- touch ----

    override fun onTouchEvent(event: MotionEvent): Boolean {
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                downX = event.x
                downY = event.y
                lastY = event.y
                mode = DragMode.NONE
                velocityTracker?.recycle()
                velocityTracker = VelocityTracker.obtain().also { it.addMovement(event) }
                removeCallbacks(longPressRunnable)
                postDelayed(longPressRunnable, 350)
                return true
            }

            MotionEvent.ACTION_MOVE -> {
                velocityTracker?.addMovement(event)
                val dx = event.x - downX
                val dy = event.y - downY
                when (mode) {
                    DragMode.NONE -> {
                        if (abs(dx) > touchSlop || abs(dy) > touchSlop) {
                            removeCallbacks(longPressRunnable)
                        }
                        if (abs(dy) > touchSlop && abs(dy) > abs(dx)) {
                            if (!altScreen) {
                                mode = DragMode.SCROLL
                                dragging = true
                                parent?.requestDisallowInterceptTouchEvent(true)
                                selectionAnchor = null
                                selectionFocus = null
                                beginScroll()
                                scrollDistancePx += dy
                                lastY = event.y
                                commitScrollTarget()
                                invalidate()
                            }
                        }
                    }

                    DragMode.SCROLL -> {
                        scrollDistancePx += event.y - lastY
                        lastY = event.y
                        commitScrollTarget()
                        invalidate()
                    }

                    DragMode.SELECT -> {
                        cellAt(event.x, event.y)?.let {
                            selectionFocus = it
                            invalidate()
                        }
                    }
                }
                return true
            }

            MotionEvent.ACTION_UP -> {
                removeCallbacks(longPressRunnable)
                when (mode) {
                    DragMode.SCROLL -> {
                        dragging = false
                        val tracker = velocityTracker
                        tracker?.addMovement(event)
                        tracker?.computeCurrentVelocity(1000)
                        val velocity = tracker?.yVelocity ?: 0f
                        tracker?.recycle()
                        velocityTracker = null
                        if (abs(velocity) >= 60f) {
                            scrollVelocity = velocity.coerceIn(-8000f, 8000f)
                        } else {
                            settleScroll()
                        }
                    }

                    DragMode.SELECT -> {
                        performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
                    }

                    DragMode.NONE -> {
                        velocityTracker?.recycle()
                        velocityTracker = null
                        if (selectionRange() != null) {
                            selectionAnchor = null
                            selectionFocus = null
                            invalidate()
                        } else {
                            performClick()
                            if (scrollActive) settleScroll()
                            focusInput()
                        }
                    }
                }
                mode = DragMode.NONE
                return true
            }

            MotionEvent.ACTION_CANCEL -> {
                removeCallbacks(longPressRunnable)
                velocityTracker?.recycle()
                velocityTracker = null
                if (mode == DragMode.SCROLL) {
                    dragging = false
                    settleScroll()
                }
                mode = DragMode.NONE
                return true
            }
        }
        return super.onTouchEvent(event)
    }

    override fun performClick(): Boolean {
        super.performClick()
        return true
    }

    private fun startSelection(x: Float, y: Float) {
        val position = cellAt(x, y) ?: return
        mode = DragMode.SELECT
        selectionAnchor = position
        selectionFocus = position
        parent?.requestDisallowInterceptTouchEvent(true)
        performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
        invalidate()
    }

    private fun cellAt(x: Float, y: Float): CellPos? {
        if (cols <= 0 || rows <= 0 || cellWidth <= 0f) return null
        val row = ((y - visualOffset()) / cellHeight).toInt().coerceIn(0, rows - 1)
        val col = (x / cellWidth).toInt().coerceIn(0, cols - 1)
        return CellPos(row, col)
    }

    // ---- hardware keys ----

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (!isFocused) return super.onKeyDown(keyCode, event)
        val bytes = KeyEncoder.encode(event, appCursorKeys)
        if (bytes != null) {
            send(bytes)
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    // ---- IME ----

    override fun onCheckIsTextEditor(): Boolean = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        // Normal text with suggestions off; the password variation makes some
        // OEM keyboards switch to their secure input method, which hides the
        // terminal beneath a screenshot-blocking overlay.
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or
            InputType.TYPE_TEXT_VARIATION_NORMAL or
            InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or
            InputType.TYPE_TEXT_FLAG_MULTI_LINE
        outAttrs.imeOptions = EditorInfo.IME_ACTION_NONE or
            EditorInfo.IME_FLAG_NO_EXTRACT_UI or
            EditorInfo.IME_FLAG_NO_FULLSCREEN
        outAttrs.initialCapsMode = 0
        return TerminalInputConnection(this)
    }

    private class TerminalInputConnection(view: TerminalView) :
        BaseInputConnection(view, true) {
        private val terminal = view

        override fun commitText(text: CharSequence?, newCursorPosition: Int): Boolean {
            val value = text?.toString().orEmpty()
            terminal.composing = ""
            if (value.isNotEmpty()) {
                val normalized = value.replace("\r\n", "\r").replace('\n', '\r')
                terminal.send(normalized.toByteArray(Charsets.UTF_8))
            }
            terminal.invalidate()
            return true
        }

        override fun setComposingText(text: CharSequence?, newCursorPosition: Int): Boolean {
            terminal.composing = text?.toString().orEmpty()
            terminal.invalidate()
            return true
        }

        override fun finishComposingText(): Boolean {
            terminal.composing = ""
            terminal.invalidate()
            return true
        }

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            if (beforeLength <= 0) return true
            val count = beforeLength.coerceAtMost(64)
            terminal.send(ByteArray(count) { 0x7F })
            return true
        }

        override fun sendKeyEvent(event: KeyEvent?): Boolean {
            if (event == null) return false
            val bytes = KeyEncoder.encode(event, terminal.appCursorKeys)
            if (bytes != null) {
                terminal.send(bytes)
                return true
            }
            return super.sendKeyEvent(event)
        }

        override fun performEditorAction(editorAction: Int): Boolean {
            terminal.send(byteArrayOf(0x0D))
            return true
        }

        override fun getTextBeforeCursor(length: Int, flags: Int): CharSequence = ""

        override fun getTextAfterCursor(length: Int, flags: Int): CharSequence = ""
    }

    // ---- drawing ----

    override fun onDraw(canvas: Canvas) {
        // A View's canvas is clipped to its parent, not to its own bounds, and
        // an interop view is drawn between the Compose siblings around it.
        // Filling the clip would paint over everything composed before this
        // view, including the session status banner.
        canvas.clipRect(0f, 0f, width.toFloat(), height.toFloat())
        canvas.drawColor(background)
        if (cols <= 0 || rows <= 0) return
        val offset = visualOffset()
        val reverse = (flags and FLAG_REVERSE_VIDEO) != 0
        val defaultFg = if (reverse) background else foreground
        val defaultBg = if (reverse) foreground else background

        for (y in 0 until rows) {
            val top = y * cellHeight + offset
            if (top > height || top + cellHeight < 0f) continue
            drawRowBackgrounds(canvas, y, top, defaultFg, defaultBg)
            drawRowText(canvas, y, top + textBaselineShift, defaultFg, defaultBg)
        }
        drawSelection(canvas, offset)
        drawCursor(canvas, offset, defaultFg)
        drawPreedit(canvas, offset)
    }

    private fun drawRowBackgrounds(
        canvas: Canvas,
        row: Int,
        top: Float,
        defaultFg: Int,
        defaultBg: Int,
    ) {
        var runStart = 0
        var runColor = background
        var drawing = false
        val base = row * cols
        val count = rowCounts[row]
        for (x in 0 until cols) {
            val color = if (x < count) backgroundFor(base + x, defaultFg, defaultBg) else defaultBg
            if (color != runColor) {
                if (drawing && x > runStart) {
                    backgroundPaint.color = runColor
                    canvas.drawRect(
                        runStart * cellWidth,
                        top,
                        x * cellWidth,
                        top + cellHeight,
                        backgroundPaint,
                    )
                }
                runStart = x
                runColor = color
                drawing = color != background
            }
        }
        if (drawing) {
            backgroundPaint.color = runColor
            canvas.drawRect(
                runStart * cellWidth,
                top,
                cols * cellWidth,
                top + cellHeight,
                backgroundPaint,
            )
        }
    }

    private fun backgroundFor(index: Int, defaultFg: Int, defaultBg: Int): Int {
        val attr = cellAttr[index]
        val inverse = (attr and ATTR_INVERSE) != 0
        return if (inverse) {
            colorOf(cellFg[index], defaultFg)
        } else {
            colorOf(cellBg[index], defaultBg)
        }
    }

    private fun drawRowText(
        canvas: Canvas,
        row: Int,
        baseline: Float,
        defaultFg: Int,
        defaultBg: Int,
    ) {
        val base = row * cols
        val count = rowCounts[row]
        val run = StringBuilder()
        var runStart = 0
        var runColor = 0
        var runStyle = -1

        fun flush(endColumn: Int) {
            if (run.isEmpty()) return
            configureTextPaint(runColor, runStyle)
            canvas.drawText(run.toString(), runStart * cellWidth, baseline, textPaint)
            run.setLength(0)
            runStyle = -1
        }

        for (x in 0 until count) {
            val index = base + x
            val attr = cellAttr[index]
            if ((cellFlags[index].toInt() and CELL_FLAG_WIDE_TAIL) != 0) continue
            if ((attr and ATTR_HIDDEN) != 0) {
                flush(x)
                continue
            }
            val codePoint = cellRune[index]
            if (codePoint == 0 || !Character.isValidCodePoint(codePoint)) {
                flush(x)
                continue
            }
            val inverse = (attr and ATTR_INVERSE) != 0
            val fg = colorOf(cellFg[index], defaultFg)
            val bg = colorOf(cellBg[index], defaultBg)
            var color = if (inverse) bg else fg
            if ((attr and ATTR_DIM) != 0) color = dim(color)
            val style = attr and STYLE_MASK
            val wide = cellWidths[index].toInt() == 2
            if (style != runStyle || color != runColor || (wide && run.isNotEmpty())) {
                flush(x)
            }
            if (run.isEmpty()) {
                runStart = x
                runColor = color
                runStyle = style
            }
            run.appendCodePoint(codePoint)
            if (wide) flush(x + 1)
        }
        flush(count)
    }

    private fun configureTextPaint(color: Int, style: Int) {
        textPaint.color = color
        textPaint.isFakeBoldText = (style and ATTR_BOLD) != 0
        textPaint.textSkewX = if ((style and ATTR_ITALIC) != 0) -0.25f else 0f
        textPaint.isUnderlineText = (style and ATTR_UNDERLINE) != 0
        textPaint.isStrikeThruText = (style and ATTR_STRIKE) != 0
    }

    private fun drawSelection(canvas: Canvas, offset: Float) {
        val range = selectionRange() ?: return
        val (start, end) = range
        for (row in start.row..end.row) {
            if (row >= rows) break
            val first = if (row == start.row) start.col else 0
            val last = if (row == end.row) end.col else cols - 1
            if (last < first) continue
            val top = row * cellHeight + offset
            canvas.drawRect(
                first * cellWidth,
                top,
                (last + 1) * cellWidth,
                top + cellHeight,
                selectionPaint,
            )
        }
    }

    private fun drawCursor(canvas: Canvas, offset: Float, defaultFg: Int) {
        if (!cursorVisible || !isCursorOnScreen()) return
        if (cursorY >= rows || cursorX < 0) return
        val rowStart = cursorY * cols
        if (cursorX >= rowCounts[cursorY]) return
        val index = rowStart + cursorX
        if ((cellFlags[index].toInt() and CELL_FLAG_WIDE_TAIL) != 0) return
        val widthCells = if (cellWidths[index].toInt() == 2) 2 else 1
        val x = cursorX * cellWidth
        val y = cursorY * cellHeight + offset
        val cursorColor = (defaultFg and 0x00FFFFFF) or 0x8C000000.toInt()
        cursorPaint.color = cursorColor
        when (cursorStyle) {
            1 -> canvas.drawRect(x, y + cellHeight - 2f, x + widthCells * cellWidth, y + cellHeight, cursorPaint)
            2 -> canvas.drawRect(x, y, x + 2f, y + cellHeight, cursorPaint)
            else -> canvas.drawRect(x, y, x + widthCells * cellWidth, y + cellHeight, cursorPaint)
        }
    }

    private fun drawPreedit(canvas: Canvas, offset: Float) {
        if (composing.isEmpty() || !isFocused || !isCursorOnScreen()) return
        val x = cursorX * cellWidth
        val top = cursorY * cellHeight + offset
        val baseline = top + textBaselineShift
        val width = preeditPaint.measureText(composing)
        backgroundPaint.color = 0xCC1C1F27.toInt()
        canvas.drawRect(x, top, x + maxOf(width, cellWidth), top + cellHeight, backgroundPaint)
        canvas.drawText(composing, x, baseline, preeditPaint)
    }

    private fun isCursorOnScreen(): Boolean = viewOffset == 0 && cursorY in 0 until rows

    private fun colorOf(raw: Int, fallback: Int): Int = when (raw ushr 24) {
        0 -> fallback
        1 -> palette[raw and 0xFF]
        2 -> 0xFF000000.toInt() or (raw and 0xFFFFFF)
        else -> fallback
    }

    private fun dim(color: Int): Int = (color and 0x00FFFFFF) or 0x99000000.toInt()

    private fun selectionRange(): Pair<CellPos, CellPos>? {
        val anchor = selectionAnchor ?: return null
        val focus = selectionFocus ?: return null
        return if (anchor.row < focus.row || (anchor.row == focus.row && anchor.col <= focus.col)) {
            anchor to focus
        } else {
            focus to anchor
        }
    }

    private fun selectedText(): String? {
        val range = selectionRange() ?: return null
        val (start, end) = range
        val lines = ArrayList<String>(end.row - start.row + 1)
        for (row in start.row..end.row) {
            if (row >= rows) break
            val first = if (row == start.row) start.col else 0
            val last = if (row == end.row) end.col else cols - 1
            lines.add(rowText(row, first, last).trimEnd(' '))
        }
        return lines.joinToString("\n")
    }

    private fun visibleText(): String {
        if (cols <= 0 || rows <= 0) return ""
        val lines = ArrayList<String>(rows)
        for (row in 0 until rows) {
            lines.add(rowText(row, 0, cols - 1).trimEnd(' '))
        }
        return lines.joinToString("\n").trimEnd('\n', ' ')
    }

    private fun rowText(row: Int, first: Int, last: Int): String {
        val count = rowCounts[row]
        val base = row * cols
        val builder = StringBuilder(last - first + 1)
        for (column in first..last) {
            if (column >= count) {
                builder.append(' ')
                continue
            }
            val index = base + column
            if ((cellFlags[index].toInt() and CELL_FLAG_WIDE_TAIL) != 0) continue
            val codePoint = cellRune[index]
            if (codePoint == 0 || !Character.isValidCodePoint(codePoint)) {
                builder.append(' ')
            } else {
                builder.appendCodePoint(codePoint)
            }
        }
        return builder.toString()
    }

    companion object {
        private const val HEADER_SIZE = 48
        private const val CELL_SIZE = 16
        private const val MAX_SNAPSHOT_BYTES = 16 shl 20
        private const val MAX_PASTE_CHARS = 256 * 1024

        private const val FLAG_ALT_SCREEN = 1
        private const val FLAG_CURSOR_VISIBLE = 1 shl 1
        private const val FLAG_BRACKETED_PASTE = 1 shl 3
        private const val FLAG_APP_CURSOR_KEYS = 1 shl 4
        private const val FLAG_REVERSE_VIDEO = 1 shl 5

        private const val CELL_FLAG_WIDE_TAIL = 2

        private const val ATTR_BOLD = 1
        private const val ATTR_DIM = 1 shl 1
        private const val ATTR_ITALIC = 1 shl 2
        private const val ATTR_UNDERLINE = 1 shl 3
        private const val ATTR_INVERSE = 1 shl 5
        private const val ATTR_HIDDEN = 1 shl 6
        private const val ATTR_STRIKE = 1 shl 7
        private const val STYLE_MASK =
            ATTR_BOLD or ATTR_DIM or ATTR_ITALIC or ATTR_UNDERLINE or ATTR_STRIKE

        private fun loadMonospaceTypeface(): Typeface? {
            val candidates = listOf(
                "/system/fonts/DroidSansMono.ttf",
                "/system/fonts/NotoSansMono-Regular.ttf",
                "/system/fonts/RobotoMono-Regular.ttf",
                "/system/fonts/CutiveMono.ttf",
            )
            for (path in candidates) {
                val face = try {
                    Typeface.createFromFile(path)
                } catch (_: Throwable) {
                    null
                } ?: continue
                val probe = Paint().apply {
                    typeface = face
                    textSize = 20f
                }
                if (probe.measureText("i") == probe.measureText("M")) return face
            }
            return null
        }

        private val palette = IntArray(256).also { colors ->
            val base = intArrayOf(
                0x000000, 0xCD0000, 0x00CD00, 0xCDCD00,
                0x0000EE, 0xCD00CD, 0x00CDCD, 0xE5E5E5,
                0x7F7F7F, 0xFF0000, 0x00FF00, 0xFFFF00,
                0x5C5CFF, 0xFF00FF, 0x00FFFF, 0xFFFFFF,
            )
            base.copyInto(colors)
            val levels = intArrayOf(0, 95, 135, 175, 215, 255)
            var index = 16
            for (r in levels) for (g in levels) for (b in levels) {
                colors[index++] = (r shl 16) or (g shl 8) or b
            }
            for (i in 0 until 24) {
                val v = 8 + i * 10
                colors[index++] = (v shl 16) or (v shl 8) or v
            }
            for (i in colors.indices) colors[i] = colors[i] or 0xFF000000.toInt()
        }
    }
}
