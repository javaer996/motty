package rdp

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"motty/internal/rdpcore/protocol/mcs"
	"motty/internal/rdpcore/protocol/pdu"
	"motty/internal/rdpcore/protocol/tpkt"
	"motty/internal/rdpcore/protocol/x224"
)

func TestReactivationRefreshesAfterFinalizationAndKeepsSlowPathUpdates(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if err := clientConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := serverConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Host, opts.Width, opts.Height = "test", 4, 2
	client, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	client.conn = clientConn
	client.tpktConn = tpkt.NewConn(clientConn, discardLogger)
	client.userChannelID, client.ioChannelID = 1002, 1003
	client.shareID.Store(1)
	client.framebuf = NewFramebuffer(2, 2)
	client.setState(StateConnected)

	var bitmapUpdates atomic.Int32
	client.OnBitmap(func(*BitmapUpdate) { bitmapUpdates.Add(1) })
	var resizeCalls atomic.Int32
	client.OnResize(func(width, height int) {
		resizeCalls.Add(1)
		if width != 4 || height != 2 {
			t.Errorf("resize callback = %dx%d, want 4x2", width, height)
		}
		if err := client.RefreshRect(0, 0, width, height); err != nil {
			t.Errorf("refresh after reactivation: %v", err)
		}
	})

	serverDone := make(chan error, 1)
	go func() {
		server := tpkt.NewConn(serverConn, discardLogger)
		send := func(userData []byte) error {
			mcsData := mcs.EncodeSendDataRequest(1002, 1003, userData)
			mcsData[0] = mcs.DomainMCSPDUSendDataIndication
			return server.Write(x224.EncodeDataTPDU(mcsData))
		}
		read := func(wantType uint16, wantType2 uint8) ([]byte, error) {
			pduType, pduType2, payload, err := readClientShareData(server)
			if err != nil {
				return nil, err
			}
			if pduType != wantType || pduType2 != wantType2 {
				return nil, fmt.Errorf("client PDU = type %#x/type2 %d, want %#x/%d", pduType, pduType2, wantType, wantType2)
			}
			return payload, nil
		}

		demandActive := make([]byte, 18)
		binary.LittleEndian.PutUint16(demandActive[0:2], uint16(len(demandActive)))
		binary.LittleEndian.PutUint16(demandActive[2:4], pdu.TypeDemandActive)
		binary.LittleEndian.PutUint16(demandActive[4:6], 1003)
		binary.LittleEndian.PutUint32(demandActive[6:10], 1)
		if err := send(demandActive); err != nil {
			serverDone <- err
			return
		}
		if _, err := read(pdu.TypeConfirmActive, 0); err != nil {
			serverDone <- err
			return
		}
		for _, pduType2 := range []uint8{pdu.PDUType2Synchronize, pdu.PDUType2Control, pdu.PDUType2Control} {
			if _, err := read(pdu.TypeData, pduType2); err != nil {
				serverDone <- err
				return
			}
		}

		if err := send(pdu.EncodeShareDataPDU(1, pdu.PDUType2Synchronize, 1003, pdu.EncodeSynchronize(1002))); err != nil {
			serverDone <- err
			return
		}
		if err := send(pdu.EncodeShareDataPDU(1, pdu.PDUType2Update, 1003, buildBitmapPayload([]byte{0x11, 0x22}))); err != nil {
			serverDone <- err
			return
		}
		if err := send(pdu.EncodeShareDataPDU(1, pdu.PDUType2Control, 1003, pdu.EncodeControl(pdu.ControlGrantedControl))); err != nil {
			serverDone <- err
			return
		}

		if _, err := read(pdu.TypeData, pdu.PDUType2Input); err != nil {
			serverDone <- err
			return
		}
		if _, err := read(pdu.TypeData, pdu.PDUType2FontList); err != nil {
			serverDone <- err
			return
		}
		if err := send(pdu.EncodeShareDataPDU(1, pdu.PDUType2Update, 1003, buildBitmapPayload([]byte{0x33, 0x44}))); err != nil {
			serverDone <- err
			return
		}
		if err := send(pdu.EncodeShareDataPDU(1, pdu.PDUType2FontMap, 1003, nil)); err != nil {
			serverDone <- err
			return
		}

		payload, err := read(pdu.TypeData, pdu.PDUType2RefreshRect)
		if err != nil {
			serverDone <- err
			return
		}
		if len(payload) != 12 || binary.LittleEndian.Uint16(payload[8:10]) != 3 || binary.LittleEndian.Uint16(payload[10:12]) != 1 {
			serverDone <- fmt.Errorf("refresh payload = %x, want full 4x2 desktop", payload)
			return
		}
		serverDone <- nil
	}()

	client.handleReactivation()
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if got := resizeCalls.Load(); got != 1 {
		t.Fatalf("resize callback called %d times, want once", got)
	}
	if got := bitmapUpdates.Load(); got != 2 {
		t.Fatalf("received %d slow-path bitmap callbacks during finalization, want 2", got)
	}
	if width, height := client.FramebufferDims(); width != 4 || height != 2 {
		t.Fatalf("framebuffer is %dx%d after reactivation, want 4x2", width, height)
	}
}

func readClientShareData(conn *tpkt.Conn) (uint16, uint8, []byte, error) {
	packet, err := conn.Read()
	if err != nil {
		return 0, 0, nil, err
	}
	mcsData, err := x224.DecodeDataTPDU(packet)
	if err != nil {
		return 0, 0, nil, err
	}
	if len(mcsData) < 7 {
		return 0, 0, nil, fmt.Errorf("short client MCS packet: %d", len(mcsData))
	}
	off := 6
	length := int(mcsData[off])
	if mcsData[off]&0x80 != 0 {
		if len(mcsData) < off+2 {
			return 0, 0, nil, fmt.Errorf("truncated client MCS length")
		}
		length = int(mcsData[off]&0x3f)<<8 | int(mcsData[off+1])
		off++
	}
	off++
	if off+length > len(mcsData) {
		return 0, 0, nil, fmt.Errorf("short client MCS data: have %d, need %d", len(mcsData)-off, length)
	}
	shareHeader, rest, err := pdu.DecodeShareControlHeader(discardLogger, mcsData[off:off+length])
	if err != nil {
		return 0, 0, nil, err
	}
	if shareHeader.PDUType&0x000f != pdu.TypeData&0x000f {
		return shareHeader.PDUType, 0, rest, nil
	}
	shareData, payload, err := pdu.DecodeShareDataHeader(discardLogger, rest)
	if err != nil {
		return 0, 0, nil, err
	}
	return shareHeader.PDUType, shareData.PDUType2, payload, nil
}
