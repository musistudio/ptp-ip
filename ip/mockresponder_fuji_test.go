package ip

import (
	"encoding/binary"
	"fmt"
	"github.com/malc0mn/ptp-ip/ptp"
	"io"
	"io/ioutil"
	"net"
	"os"
)

func handleFujiMessages(conn net.Conn, evtChan chan uint32, lmp string) {
	// NO defer conn.Close() here since we need to mock a real Fuji responder and thus need to keep the connections open
	// when established and continuously listen for messages in a loop.
	for {
		l, raw, err := readMessageRaw(conn, lmp)
		if err == io.EOF {
			conn.Close()
			break
		}
		if raw == nil {
			continue
		}

		lgr.Infof("%s read %d raw bytes", lmp, l)

		var (
			msg  string
			resp PacketIn
			data []byte
			evt  uint32
		)
		eodp := false

		// This construction is thanks to the Fuji decision of not properly using packet types. Watch out for the caveat
		// here: we need to swap the order of the DataPhase and the OperationRequestCode because we are reading what are
		// actually two uint16 numbers as if they were a single uint32!
		switch binary.LittleEndian.Uint32(raw[0:4]) {
		case uint32(PKT_InitCommandRequest):
			msg, resp = genericInitCommandRequestResponse(lmp, ProtocolVersion(0))
		case constructPacketType(OC_Fuji_GetCapturePreview):
			msg, resp, data = fujiGetCapturePreview(raw[4:8])
			evt = constructEventData(OC_Fuji_GetCapturePreview, raw[4:8])
			eodp = true
		case constructPacketType(OC_Fuji_GetDeviceInfo):
			msg, resp, data = fujiGetDeviceInfo(raw[4:8])
			eodp = true
		case constructPacketType(ptp.OC_GetDevicePropDesc):
			msg, resp, data = fujiGetDevicePropDescResponse(raw[4:8], raw[8:10])
			eodp = true
		case constructPacketType(ptp.OC_GetDevicePropValue):
			msg, resp, data = fujiGetDevicePropValueResponse(raw[4:8], raw[8:10])
			eodp = true
		case constructPacketType(ptp.OC_InitiateCapture):
			msg, resp = fujiInitiateCaptureResponse(raw[4:8])
			evt = constructEventData(ptp.OC_InitiateCapture, raw[4:8])
		case constructPacketType(ptp.OC_InitiateOpenCapture):
			msg, resp = fujiInitiateOpenCaptureResponse(raw[4:8])
		case constructPacketType(ptp.OC_OpenSession):
			msg, resp = fujiOpenSessionResponse(raw[4:8])
		case constructPacketTypeWithDataPhase(ptp.OC_SetDevicePropValue, DP_DataOut):
			// SetDevicePropValue involves two messages, only the second one needs a response from us!
			msg, resp = fujiSetDevicePropValue(raw[4:8])
		}

		if resp != nil {
			if msg != "" {
				lgr.Infof("%s responding to %s", lmp, msg)
			}
			sendMessage(conn, resp, data, lmp)
			if eodp {
				lgr.Infof("%s sending end of data packet", lmp)
				sendMessage(conn, fujiEndOfDataPacket(raw[4:8]), nil, lmp)
			}
		}

		if evt != 0 {
			evtChan <- evt
			lgr.Infof("%s requested event dispatch for oc|tid %#x...", lmp, evt)
		}
	}
}

func handleFujiEvents(conn net.Conn, evtChan chan uint32, lmp string) {
	for {
		var evts []*FujiEventPacket
		data := <-evtChan
		lgr.Infof("%s received event request %#x", lmp, data)
		oc := ptp.OperationCode(data & uint32(0xFFFF0000) >> 16)
		tid := ptp.TransactionID(data & uint32(0x0000FFFF))
		lgr.Infof("%s operation code %#x with transaction ID %#x", lmp, oc, tid)

		switch oc {
		case ptp.OC_InitiateCapture:
			fSize, _ := os.Stat("testdata/preview.jpg")
			evts = append(
				evts,
				&FujiEventPacket{
					DataPhase:     0x0004,
					EventCode:     EC_Fuji_ObjectAdded,
					Amount:        1, // No clue what this is, always seems to be set to 1
					TransactionID: tid,
					Parameter1:    uint32(tid), // Yes, it is always set to the transaction ID!
				},
				&FujiEventPacket{
					DataPhase:     0x0004,
					EventCode:     EC_Fuji_PreviewAvailable,
					Amount:        1, // No clue what this is, always seems to be set to 1
					TransactionID: tid,
					Parameter1:    uint32(tid), // Yes, it is always set to the transaction ID!
					Parameter2:    uint32(fSize.Size()),
				},
			)
		case OC_Fuji_GetCapturePreview:
			evts = append(evts, &FujiEventPacket{
				DataPhase:     0x0004,
				EventCode:     ptp.EC_CaptureComplete,
				Amount:        1,
				TransactionID: tid,
				Parameter1:    uint32(tid),
			})
		}

		for _, evt := range evts {
			sendMessage(conn, evt, nil, lmp)
		}
	}
}

func constructPacketType(code ptp.OperationCode) uint32 {
	return constructPacketTypeWithDataPhase(code, DP_NoDataOrDataIn)
}

func constructPacketTypeWithDataPhase(code ptp.OperationCode, dp DataPhase) uint32 {
	return uint32(code)<<16 | uint32(dp)
}

// Don't try this at home: it is fine for testing as the transaction ID will always be quite low.
func constructEventData(code ptp.OperationCode, tid []byte) uint32 {
	return uint32(code)<<16 | binary.LittleEndian.Uint32(tid)
}

func fujiGetCapturePreview(tid []byte) (string, *FujiOperationResponsePacket, []byte) {
	dat, _ := ioutil.ReadFile("testdata/preview.jpg")
	return "GetCapturePreview",
		fujiOperationResponsePacket(DP_DataOut, RC_Fuji_GetCapturePreview, tid),
		dat
}

func fujiGetDeviceInfo(tid []byte) (string, *FujiOperationResponsePacket, []byte) {
	return "GetDeviceInfo",
		fujiOperationResponsePacket(DP_DataOut, RC_Fuji_GetDeviceInfo, tid),
		[]byte{
			0x08, 0x00, 0x00, 0x00, 0x16, 0x00, 0x00, 0x00, 0x12, 0x50, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x02,
			0x03, 0x00, 0x00, 0x00, 0x02, 0x00, 0x04, 0x00, 0x14, 0x00, 0x00, 0x00, 0x0c, 0x50, 0x04, 0x00, 0x01, 0x02,
			0x00, 0x09, 0x80, 0x02, 0x02, 0x00, 0x09, 0x80, 0x0a, 0x80, 0x24, 0x00, 0x00, 0x00, 0x05, 0x50, 0x04, 0x00,
			0x01, 0x02, 0x00, 0x02, 0x00, 0x02, 0x0a, 0x00, 0x02, 0x00, 0x04, 0x00, 0x06, 0x80, 0x01, 0x80, 0x02, 0x80,
			0x03, 0x80, 0x06, 0x00, 0x0a, 0x80, 0x0b, 0x80, 0x0c, 0x80, 0x36, 0x00, 0x00, 0x00, 0x10, 0x50, 0x03, 0x00,
			0x01, 0x00, 0x00, 0x00, 0x00, 0x02, 0x13, 0x00, 0x48, 0xf4, 0x95, 0xf5, 0xe3, 0xf6, 0x30, 0xf8, 0x7d, 0xf9,
			0xcb, 0xfa, 0x18, 0xfc, 0x65, 0xfd, 0xb3, 0xfe, 0x00, 0x00, 0x4d, 0x01, 0x9b, 0x02, 0xe8, 0x03, 0x35, 0x05,
			0x83, 0x06, 0xd0, 0x07, 0x1d, 0x09, 0x6b, 0x0a, 0xb8, 0x0b, 0x26, 0x00, 0x00, 0x00, 0x01, 0xd0, 0x04, 0x00,
			0x01, 0x01, 0x00, 0x02, 0x00, 0x02, 0x0b, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03, 0x00, 0x04, 0x00, 0x05, 0x00,
			0x06, 0x00, 0x07, 0x00, 0x08, 0x00, 0x09, 0x00, 0x0a, 0x00, 0x0b, 0x00, 0x78, 0x00, 0x00, 0x00, 0x2a, 0xd0,
			0x06, 0x00, 0x01, 0xff, 0xff, 0xff, 0xff, 0x00, 0x19, 0x00, 0x80, 0x02, 0x19, 0x00, 0x90, 0x01, 0x00, 0x80,
			0x20, 0x03, 0x00, 0x80, 0x40, 0x06, 0x00, 0x80, 0x80, 0x0c, 0x00, 0x80, 0x00, 0x19, 0x00, 0x80, 0x64, 0x00,
			0x00, 0x40, 0xc8, 0x00, 0x00, 0x00, 0xfa, 0x00, 0x00, 0x00, 0x40, 0x01, 0x00, 0x00, 0x90, 0x01, 0x00, 0x00,
			0xf4, 0x01, 0x00, 0x00, 0x80, 0x02, 0x00, 0x00, 0x20, 0x03, 0x00, 0x00, 0xe8, 0x03, 0x00, 0x00, 0xe2, 0x04,
			0x00, 0x00, 0x40, 0x06, 0x00, 0x00, 0xd0, 0x07, 0x00, 0x00, 0xc4, 0x09, 0x00, 0x00, 0x80, 0x0c, 0x00, 0x00,
			0xa0, 0x0f, 0x00, 0x00, 0x88, 0x13, 0x00, 0x00, 0x00, 0x19, 0x00, 0x00, 0x00, 0x32, 0x00, 0x40, 0x00, 0x64,
			0x00, 0x40, 0x00, 0xc8, 0x00, 0x40, 0x14, 0x00, 0x00, 0x00, 0x19, 0xd0, 0x04, 0x00, 0x01, 0x01, 0x00, 0x01,
			0x00, 0x02, 0x02, 0x00, 0x00, 0x00, 0x01, 0x00, 0x1e, 0x00, 0x00, 0x00, 0x7c, 0xd1, 0x06, 0x00, 0x01, 0x00,
			0x00, 0x00, 0x00, 0x02, 0x07, 0x02, 0x03, 0x01, 0x00, 0x00, 0x00, 0x00, 0x07, 0x07, 0x09, 0x10, 0x01, 0x00,
			0x00, 0x00,
		}
}

func fujiGetDevicePropDescResponse(tid []byte, prop []byte) (string, *FujiOperationResponsePacket, []byte) {
	var p []byte

	switch binary.LittleEndian.Uint16(prop) {
	case uint16(DPC_Fuji_FilmSimulation):
		p = []byte{0x01, 0xd0, 0x04, 0x00, 0x01, 0x01, 0x00, 0x01, 0x00, 0x02, 0x0b, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03,
			0x00, 0x04, 0x00, 0x05, 0x00, 0x06, 0x00, 0x07, 0x00, 0x08, 0x00, 0x09, 0x00, 0x0a, 0x00, 0x0b, 0x00,
		}
	}

	return fmt.Sprintf("GetDevicePropDesc %#x", binary.LittleEndian.Uint16(prop)),
		fujiOperationResponsePacket(DP_DataOut, RC_Fuji_GetDevicePropDesc, tid),
		p
}

func fujiGetDevicePropValueResponse(tid []byte, prop []byte) (string, *FujiOperationResponsePacket, []byte) {
	var p []byte

	switch binary.LittleEndian.Uint16(prop) {
	case uint16(DPC_Fuji_AppVersion):
		p = make([]byte, 4)
		binary.LittleEndian.PutUint32(p, PM_Fuji_AppVersion)
	case uint16(DPC_Fuji_CurrentState):
		p = []byte{0x11, 0x00, 0x01, 0x50, 0x02, 0x00, 0x00, 0x00, 0x41, 0xd2, 0x0a, 0x00, 0x00, 0x00, 0x05, 0x50, 0x02,
			0x00, 0x00, 0x00, 0x0a, 0x50, 0x01, 0x80, 0x00, 0x00, 0x0c, 0x50, 0x0a, 0x80, 0x00, 0x00, 0x0e, 0x50, 0x02,
			0x00, 0x00, 0x00, 0x10, 0x50, 0xb3, 0xfe, 0x00, 0x00, 0x12, 0x50, 0x00, 0x00, 0x00, 0x00, 0x01, 0xd0, 0x02,
			0x00, 0x00, 0x00, 0x18, 0xd0, 0x04, 0x00, 0x00, 0x00, 0x28, 0xd0, 0x00, 0x00, 0x00, 0x00, 0x2a, 0xd0, 0x00,
			0x19, 0x00, 0x80, 0x7c, 0xd1, 0x02, 0x07, 0x02, 0x03, 0x09, 0xd2, 0x00, 0x00, 0x00, 0x00, 0x1b, 0xd2, 0x00,
			0x00, 0x00, 0x00, 0x29, 0xd2, 0xd6, 0x05, 0x00, 0x00, 0x2a, 0xd2, 0x8f, 0x06, 0x00, 0x00,
		}
	}

	return fmt.Sprintf("GetDevicePropValue %#x", binary.LittleEndian.Uint16(prop)),
		fujiOperationResponsePacket(DP_DataOut, RC_Fuji_GetDevicePropValue, tid),
		p
}

func fujiInitiateCaptureResponse(tid []byte) (string, *FujiOperationResponsePacket) {
	return "InitiateCapture",
		fujiEndOfDataPacket(tid)
}

func fujiInitiateOpenCaptureResponse(tid []byte) (string, *FujiOperationResponsePacket) {
	return "InitiateOpenCapture",
		fujiEndOfDataPacket(tid)
}

func fujiOpenSessionResponse(tid []byte) (string, *FujiOperationResponsePacket) {
	return "OpenSession",
		fujiEndOfDataPacket(tid)
}

func fujiSetDevicePropValue(tid []byte) (string, *FujiOperationResponsePacket) {
	return "SetDevicePropValue",
		fujiEndOfDataPacket(tid)
}

func fujiEndOfDataPacket(tid []byte) *FujiOperationResponsePacket {
	return fujiOperationResponsePacket(DP_Unknown, ptp.RC_OK, tid)
}

func fujiOperationResponsePacket(dp DataPhase, orc ptp.OperationResponseCode, tid []byte) *FujiOperationResponsePacket {
	return &FujiOperationResponsePacket{
		DataPhase:             uint16(dp),
		OperationResponseCode: orc,
		TransactionID:         ptp.TransactionID(binary.LittleEndian.Uint32(tid)),
	}
}
