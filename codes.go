package tx510

import (
	"errors"
	"fmt"
)

type CmdID byte

const (
	CmdRecognize       CmdID = 0x12
	CmdRegister        CmdID = 0x13
	CmdDeleteUser      CmdID = 0x20
	CmdDeleteAll       CmdID = 0x21
	CmdVersion         CmdID = 0x30
	CmdBaudRate        CmdID = 0x51
	CmdBacklight       CmdID = 0xC0
	CmdDisplay         CmdID = 0xC1
	CmdWhiteLight      CmdID = 0xC2
	CmdReboot          CmdID = 0xC3
	CmdUserCount       CmdID = 0xC4
	CmdWriteEigenvalue CmdID = 0xC5
	CmdReadEigenvalue  CmdID = 0xC6
)

type BaudRate byte

const (
	Baud9600   BaudRate = 0x00
	Baud19200  BaudRate = 0x01
	Baud38400  BaudRate = 0x02
	Baud57600  BaudRate = 0x03
	Baud115200 BaudRate = 0x04 // factory default
)

func (b BaudRate) Int() int {
	switch b {
	case Baud9600:
		return 9600
	case Baud19200:
		return 19200
	case Baud38400:
		return 38400
	case Baud57600:
		return 57600
	case Baud115200:
		return 115200
	}
	return 0
}

type ResultCode byte

const (
	ResultSuccess           ResultCode = 0x00
	ResultNoFace            ResultCode = 0x01
	ResultPoseAngleTooLarge ResultCode = 0x03
	ResultLiveness2DFailed  ResultCode = 0x06
	ResultLiveness3DFailed  ResultCode = 0x07
	ResultMatchFailed       ResultCode = 0x08
	ResultDuplicateFace     ResultCode = 0x09
)

func (c CmdID) String() string {
	switch c {
	case CmdRecognize:
		return "Recognise face"
	case CmdRegister:
		return "Register face"
	case CmdDeleteUser:
		return "Delete user"
	case CmdDeleteAll:
		return "Delete all faces"
	case CmdVersion:
		return "Version"
	case CmdBaudRate:
		return "BaudRate"
	case CmdBacklight:
		return "Backlight"
	case CmdDisplay:
		return "Display"
	case CmdWhiteLight:
		return "White Light"
	case CmdReboot:
		return "Reboot tx510"
	case CmdUserCount:
		return "User count"
	case CmdWriteEigenvalue:
		return "Write Eigen value"
	case CmdReadEigenvalue:
		return "Read Eigen value"
	default:
		return fmt.Sprintf("unknown(0x%02x)", byte(c))
	}
}

func (r ResultCode) String() string {
	switch r {
	case ResultSuccess:
		return "Success"
	case ResultNoFace:
		return "No face detected"
	case ResultPoseAngleTooLarge:
		return "Face too far"
	case ResultLiveness2DFailed:
		return "2D Liveness check failed"
	case ResultLiveness3DFailed:
		return "3D Liveness check failed"
	case ResultMatchFailed:
		return "Match failed"
	case ResultDuplicateFace:
		return "Face duplication"
	default:
		return fmt.Sprintf("unknown(0x%02x)", byte(r))
	}
}

func (r ResultCode) IsSuccess() bool { return r == ResultSuccess }

// IsLivenessError reports whether err is a ResultLiveness2DFailed or ResultLiveness3DFailed device reject.
func IsLivenessError(err error) bool {
	var rerr *ResultError
	if !errors.As(err, &rerr) {
		return false
	}
	return rerr.Code == ResultLiveness2DFailed || rerr.Code == ResultLiveness3DFailed
}
