package tx510_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	tx510 "github.com/RamziEKhoury/go-hlk-tx510"
)

// Open a TX510 on the default baud rate (115200) and print its firmware
// version.
func ExampleNew() {
	client, err := tx510.New("/dev/ttyUSB0")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	version, err := client.Version(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(version)
}

// Open a TX510 at a non-default baud rate.
func ExampleNew_customBaud() {
	client, err := tx510.New("/dev/ttyUSB0", tx510.WithBaud(57600))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	_ = client
}

// Enroll the face currently in view and print its assigned faceID. Enrollment
// can take several seconds, so use a generous context deadline.
func ExampleClient_Register() {
	client, err := tx510.New("/dev/ttyUSB0")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	faceID, err := client.Register(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("enrolled faceID:", faceID)
}

// Recognize a face and branch on the device's result code.
func ExampleClient_Recognize() {
	client, err := tx510.New("/dev/ttyUSB0")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	faceID, err := client.Recognize(ctx)
	switch {
	case err == nil:
		fmt.Println("matched faceID:", faceID)
	case isResult(err, tx510.ResultNoFace):
		fmt.Println("no face presented")
	case isResult(err, tx510.ResultMatchFailed):
		fmt.Println("face not enrolled")
	default:
		log.Fatal(err)
	}
}

func isResult(err error, code tx510.ResultCode) bool {
	var rerr *tx510.ResultError
	return errors.As(err, &rerr) && rerr.Code == code
}
