package gocosem

import (
	"fmt"
	"testing"
	"time"
)

var hdlcTestMeterIp = "172.16.123.182"

var hdlcTestResponseTimeout = time.Duration(1) * time.Hour
var hdlcTestCosemWaitTime = time.Duration(5000) * time.Millisecond
var hdlcTestSnrmTimeout = time.Duration(45) * time.Second
var hdlcTestDiscTimeout = time.Duration(45) * time.Second

func TestMeterHdlc_with_sec_5_TcpConnect(t *testing.T) {
	dconn, err := TcpConnect(hdlcTestMeterIp, 4059)
	if nil != err {
		t.Fatal(err)
	}
	t.Logf("transport connected")
	defer dconn.Close()
}

func TestMeterHdlc_with_sec_5_HdlcConnect(t *testing.T) {
	dconn, err := HdlcConnect(hdlcTestMeterIp, 4059, 1, 1, nil, hdlcTestResponseTimeout, &hdlcTestCosemWaitTime, hdlcTestSnrmTimeout, hdlcTestDiscTimeout)
	if nil != err {
		t.Fatal(err)
	}
	t.Logf("transport connected")
	defer dconn.Close()
}

func TestMeterHdlc_with_sec_5_AppConnect(t *testing.T) {

	dconn, err := HdlcConnect(hdlcTestMeterIp, 4059, 1, 1, nil, hdlcTestResponseTimeout, &hdlcTestCosemWaitTime, hdlcTestSnrmTimeout, hdlcTestDiscTimeout)
	if nil != err {
		t.Fatal(err)
	}
	t.Logf("transport connected")
	defer dconn.Close()

	aconn, err := dconn.AppConnectWithSecurity5(01, 01, 4, []uint32{2, 16, 756, 5, 8, 1, 3}, []byte{0x4D, 0x45, 0x4C, 0x00, 0x00, 0x00, 0x00, 0x01}, ")HB+0F04", []byte{0x21, 0x1F, 0x30, 0x24, 0x50, 0x7E, 0x1E, 0xC4, 0xC0, 0xDB, 0xB9, 0x52, 0xC7, 0x0E, 0x7B, 0x3F, 0xF0, 0xA2, 0x96, 0x2B, 0xB8, 0x86, 0x5A, 0xB9, 0xE5, 0x67, 0xA0, 0xC3, 0x81, 0xD6, 0xEB, 0xF5, 0xC3})
	if nil != err {
		t.Fatalf(fmt.Sprintf("%s\n", err))
	}
	t.Logf("application connected")
	defer aconn.Close()
}