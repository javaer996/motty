package dev.motty.app.ui.terminal

import android.view.KeyEvent

/**
 * Turns hardware key events into the byte sequences a terminal expects. The
 * mapping mirrors what iOS's key commands send; modal editing keys follow the
 * screen's application-cursor-keys flag.
 */
object KeyEncoder {
    private val ESC = byteArrayOf(0x1B)

    fun encode(event: KeyEvent, appCursorKeys: Boolean): ByteArray? {
        if (event.action != KeyEvent.ACTION_DOWN) return null
        val ctrl = event.isCtrlPressed
        val alt = event.isAltPressed

        if (ctrl && !alt) {
            controlByte(event.keyCode)?.let { return byteArrayOf(it) }
        }

        special(event.keyCode, event.isShiftPressed, appCursorKeys)?.let { return it }

        val unicode = event.unicodeChar
        if (unicode == 0) return null
        val text = String(Character.toChars(unicode)).toByteArray(Charsets.UTF_8)
        if (alt && !ctrl) return ESC + text
        return text
    }

    private fun controlByte(keyCode: Int): Byte? = when (keyCode) {
        KeyEvent.KEYCODE_A -> 1
        KeyEvent.KEYCODE_B -> 2
        KeyEvent.KEYCODE_C -> 3
        KeyEvent.KEYCODE_D -> 4
        KeyEvent.KEYCODE_E -> 5
        KeyEvent.KEYCODE_F -> 6
        KeyEvent.KEYCODE_G -> 7
        KeyEvent.KEYCODE_H -> 8
        KeyEvent.KEYCODE_I -> 9
        KeyEvent.KEYCODE_J -> 10
        KeyEvent.KEYCODE_K -> 11
        KeyEvent.KEYCODE_L -> 12
        KeyEvent.KEYCODE_M -> 13
        KeyEvent.KEYCODE_N -> 14
        KeyEvent.KEYCODE_O -> 15
        KeyEvent.KEYCODE_P -> 16
        KeyEvent.KEYCODE_Q -> 17
        KeyEvent.KEYCODE_R -> 18
        KeyEvent.KEYCODE_S -> 19
        KeyEvent.KEYCODE_T -> 20
        KeyEvent.KEYCODE_U -> 21
        KeyEvent.KEYCODE_V -> 22
        KeyEvent.KEYCODE_W -> 23
        KeyEvent.KEYCODE_X -> 24
        KeyEvent.KEYCODE_Y -> 25
        KeyEvent.KEYCODE_Z -> 26
        KeyEvent.KEYCODE_SPACE -> 0
        KeyEvent.KEYCODE_LEFT_BRACKET -> 0x1B
        KeyEvent.KEYCODE_BACKSLASH -> 0x1C
        KeyEvent.KEYCODE_RIGHT_BRACKET -> 0x1D
        else -> null
    }

    private fun special(keyCode: Int, shift: Boolean, appCursorKeys: Boolean): ByteArray? {
        val prefix = if (appCursorKeys) "O" else "["
        return when (keyCode) {
            KeyEvent.KEYCODE_DPAD_UP -> esc(prefix + "A")
            KeyEvent.KEYCODE_DPAD_DOWN -> esc(prefix + "B")
            KeyEvent.KEYCODE_DPAD_RIGHT -> esc(prefix + "C")
            KeyEvent.KEYCODE_DPAD_LEFT -> esc(prefix + "D")
            KeyEvent.KEYCODE_MOVE_HOME -> esc(prefix + "H")
            KeyEvent.KEYCODE_MOVE_END -> esc(prefix + "F")
            KeyEvent.KEYCODE_PAGE_UP -> esc("[5~")
            KeyEvent.KEYCODE_PAGE_DOWN -> esc("[6~")
            KeyEvent.KEYCODE_INSERT -> esc("[2~")
            KeyEvent.KEYCODE_FORWARD_DEL -> esc("[3~")
            KeyEvent.KEYCODE_DEL -> byteArrayOf(0x7F)
            KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> byteArrayOf(0x0D)
            KeyEvent.KEYCODE_TAB -> if (shift) esc("[Z") else byteArrayOf(0x09)
            KeyEvent.KEYCODE_ESCAPE -> byteArrayOf(0x1B)
            KeyEvent.KEYCODE_F1 -> esc("OP")
            KeyEvent.KEYCODE_F2 -> esc("OQ")
            KeyEvent.KEYCODE_F3 -> esc("OR")
            KeyEvent.KEYCODE_F4 -> esc("OS")
            KeyEvent.KEYCODE_F5 -> esc("[15~")
            KeyEvent.KEYCODE_F6 -> esc("[17~")
            KeyEvent.KEYCODE_F7 -> esc("[18~")
            KeyEvent.KEYCODE_F8 -> esc("[19~")
            KeyEvent.KEYCODE_F9 -> esc("[20~")
            KeyEvent.KEYCODE_F10 -> esc("[21~")
            KeyEvent.KEYCODE_F11 -> esc("[23~")
            KeyEvent.KEYCODE_F12 -> esc("[24~")
            else -> null
        }
    }

    /** Escape sequences used by the on-screen key bar. */
    fun sequence(suffix: String): ByteArray = ESC + suffix.toByteArray(Charsets.US_ASCII)

    fun cursor(direction: Char, appCursorKeys: Boolean): ByteArray =
        sequence((if (appCursorKeys) "O" else "[") + direction)
}

private fun esc(sequence: String): ByteArray =
    byteArrayOf(0x1B) + sequence.toByteArray(Charsets.US_ASCII)
