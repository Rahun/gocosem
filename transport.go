package gocosem

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	Transport_TCP  = int(1)
	Transport_UDP  = int(2)
	Transport_HDLC = int(3)
)

const (
	lowest_level_security_mechanism           = int(0)
	low_level_security_mechanism              = int(1)
	high_level_security_mechanism             = int(2)
	high_level_security_mechanism_using_MD5   = int(3)
	high_level_security_mechanism_using_SHA_1 = int(4)
	high_level_security_mechanism_using_GMAC  = int(5)
)

var (
	ErrDlmsTimeout      = errors.New("dlms timeout")
	ErrUnknownTransport = errors.New("unknown dlms transport")
)

type DlmsMessage struct {
	Err  error
	Data interface{}
}

type DlmsChannel chan *DlmsMessage
type DlmsReplyChannel <-chan *DlmsMessage

type tWrapperHeader struct {
	ProtocolVersion uint16
	SrcWport        uint16
	DstWport        uint16
	DataLength      uint16
}

/*func (header *tWrapperHeader) String() string {
	return fmt.Sprintf("tWrapperHeader %+v", header)
}*/

type DlmsConn struct {
	closed                    bool
	closedMutex               sync.Mutex
	rwc                       io.ReadWriteCloser
	hdlcRwc                   io.ReadWriteCloser // stream used by hdlc transport for sending and reading HDLC frames
	HdlcClient                *HdlcTransport
	transportType             int
	hdlcResponseTimeout       time.Duration
	snrmTimeout               time.Duration
	discTimeout               time.Duration
	clientSystemTitle         []byte
	serverSystemTitle         []byte
	authenticationMechanismId int
	AK                        []byte // authentication key
	EK                        []byte // encryption key
	sendFrameCounter          uint32
	clientToServerChallenge   string
	serverToClientChallenge   string
}

type DlmsTransportSendRequest struct {
	ch  chan *DlmsMessage // reply channel
	src uint16            // source address
	dst uint16            // destination address
	pdu []byte
}
type DlmsTransportSendRequestReply struct {
}

type DlmsTransportReceiveRequest struct {
	ch  chan *DlmsMessage // reply channel
	src uint16            // source address
	dst uint16            // destination address
}
type DlmsTransportReceiveRequestReply struct {
	src uint16 // source address
	dst uint16 // destination address
	pdu []byte
}

func makeWpdu(srcWport uint16, dstWport uint16, pdu []byte) (err error, wpdu []byte) {
	var (
		buf    bytes.Buffer
		header tWrapperHeader
	)

	header.ProtocolVersion = 0x00001
	header.SrcWport = srcWport
	header.DstWport = dstWport
	header.DataLength = uint16(len(pdu))

	err = binary.Write(&buf, binary.BigEndian, &header)
	if nil != err {
		errorLog(" binary.Write() failed, err: %v\n", err)
		return err, nil
	}
	_, err = buf.Write(pdu)
	if nil != err {
		errorLog(" binary.Write() failed, err: %v\n", err)
		return err, nil
	}
	return nil, buf.Bytes()

}

func ipTransportSend(rwc io.ReadWriteCloser, srcWport uint16, dstWport uint16, pdu []byte) error {
	err, wpdu := makeWpdu(srcWport, dstWport, pdu)
	if nil != err {
		return err
	}
	debugLog("sending pdu: % 02X\n", wpdu)
	_, err = rwc.Write(wpdu)
	if nil != err {
		errorLog("io.Write() failed, err: %v\n", err)
		return err
	}
	debugLog("sending: ok")
	return nil
}

func hdlcTransportSend(rwc io.ReadWriteCloser, pdu []byte) error {
	var buf bytes.Buffer
	llcHeader := []byte{0xE6, 0xE6, 0x00} // LLC sublayer header

	_, err := buf.Write(llcHeader)
	if nil != err {
		errorLog("io.Write() failed, err: %v\n", err)
		return err
	}
	_, err = buf.Write(pdu)
	if nil != err {
		errorLog("io.Write() failed, err: %v\n", err)
		return err
	}

	p := buf.Bytes()
	debugLog("sending pdu: % 02X\n", p[3:])
	_, err = rwc.Write(p)
	if nil != err {
		errorLog("io.Write() failed, err: %v\n", err)
		return err
	}

	debugLog("sending: ok")
	return nil
}

func (dconn *DlmsConn) transportSend(src uint16, dst uint16, pdu []byte) (err error) {
	debugLog("trnasport type: %d, src: %d, dst: %d\n", dconn.transportType, src, dst)

	debugLog("sending app pdu: % 02X\n", pdu)

	err, pdu = dconn.encryptPdu(pdu)
	if nil != err {
		return err
	}

	if (Transport_TCP == dconn.transportType) || (Transport_UDP == dconn.transportType) {
		return ipTransportSend(dconn.rwc, src, dst, pdu)
	} else if Transport_HDLC == dconn.transportType {
		return hdlcTransportSend(dconn.rwc, pdu)
	} else {
		panic(fmt.Sprintf("unsupported transport type: %d", dconn.transportType))
	}
}

func ipTransportReceive(rwc io.ReadWriteCloser, srcWport *uint16, dstWport *uint16) (pdu []byte, src uint16, dst uint16, err error) {
	var (
		header tWrapperHeader
	)

	debugLog("receiving pdu ...\n")
	err = binary.Read(rwc, binary.BigEndian, &header)
	if nil != err {
		errorLog("binary.Read() failed, err: %v\n", err)
		return nil, 0, 0, err
	}
	debugLog("header: ok\n")
	if (nil != srcWport) && (header.SrcWport != *srcWport) {
		err = fmt.Errorf("wrong srcWport: %d, expected: %d", header.SrcWport, srcWport)
		errorLog("%s", err)
		return nil, 0, 0, err
	}
	if (nil != dstWport) && (header.DstWport != *dstWport) {
		err = fmt.Errorf("wrong dstWport: %d, expected: %d", header.DstWport, dstWport)
		errorLog("%s", err)
		return nil, 0, 0, err
	}
	pdu = make([]byte, header.DataLength)
	err = binary.Read(rwc, binary.BigEndian, pdu)
	if nil != err {
		errorLog("binary.Read() failed, err: %v\n", err)
		return nil, 0, 0, err
	}
	debugLog("received pdu: % 02X\n", pdu)

	return pdu, header.SrcWport, header.DstWport, nil
}

func hdlcTransportReceive(rwc io.ReadWriteCloser) (pdu []byte, err error) {

	debugLog("receiving pdu ...\n")

	//TODO: Set maxSegmnetSize to AARE.user-information.server-max-receive-pdu-size.
	// AARE.user-information is of 'InitiateResponse' asn1 type and is A-XDR encoded.
	maxSegmnetSize := 3 * 1024

	p := make([]byte, maxSegmnetSize)

	// hdlc ReadWriter read returns always whole segment into 'p' or full 'p' if 'p' is not long enough to fit in all segment
	n, err := rwc.Read(p)
	if nil != err {
		errorLog("hdlc.Read() failed, err: %v\n", err)
		return nil, err
	}
	// Guard against read buffer being shorter then maximum possible segment size.
	if len(p) == n {
		panic("short read suspected, increase buffer size!")
	}

	buf := bytes.NewBuffer(p[0:n])

	llcHeader := make([]byte, 3) // LLC sublayer header
	err = binary.Read(buf, binary.BigEndian, llcHeader)
	if nil != err {
		errorLog("binary.Read() failed, err: %v\n", err)
		return nil, err
	}
	if !bytes.Equal(llcHeader, []byte{0xE6, 0xE7, 0x00}) {
		err = fmt.Errorf("wrong LLC header")
		errorLog("%s", err)
		return nil, err
	}
	debugLog("LLC header: ok\n")

	pdu = buf.Bytes()
	debugLog("received pdu: % 02X\n", pdu)

	return pdu, nil
}

func (dconn *DlmsConn) transportReceive(src uint16, dst uint16) (pdu []byte, err error) {
	debugLog("trnascport type: %d\n", dconn.transportType)

	if (Transport_TCP == dconn.transportType) || (Transport_UDP == dconn.transportType) {
		pdu, _, _, err = ipTransportReceive(dconn.rwc, &src, &dst)
		if nil != err {
			return nil, err
		}
	} else if Transport_HDLC == dconn.transportType {
		pdu, err = hdlcTransportReceive(dconn.rwc)
		if nil != err {
			return nil, err
		}
	} else {
		err := fmt.Errorf("unsupported transport type: %d", dconn.transportType)
		errorLog("%s", err)
		return nil, err
	}

	debugLog("received app pdu: % 02X\n", pdu)

	err, pdu = dconn.decryptPdu(pdu)
	return pdu, err
}

var gloTagMap = map[byte]byte{
	1:   33,
	5:   37,
	6:   38,
	8:   40,
	12:  44,
	13:  45,
	14:  46,
	22:  54,
	24:  56,
	192: 200,
	193: 201,
	194: 202,
	195: 203,
	196: 204,
	197: 205,
	199: 207,
}

func cosemTagToGloTag(cosTag byte) (error, byte) {
	if glo, ok := gloTagMap[cosTag]; ok {
		return nil, glo
	}
	err := fmt.Errorf("unknown tag")
	errorLog("cosemTagToGloTag(%b): %s", cosTag, err)
	return err, 0
}

func gloTagToCosemTag(gloTag byte) (error, byte) {
	for cos, glo := range gloTagMap {
		if glo == gloTag {
			return nil, cos
		}
	}
	err := fmt.Errorf("unknown tag")
	errorLog("gloTagToCosemTag(%b): %s", gloTag, err)
	return err, 0
}

func (dconn *DlmsConn) encryptPduGSM(pdu []byte) (err error, epdu []byte) {

	// tag
	err, tag := cosemTagToGloTag(pdu[0])
	if nil != err {
		return err, nil
	}

	// security control
	SC := byte(0x30) // security control

	// frame counter
	dconn.sendFrameCounter += 1
	FC := make([]byte, 4)
	FC[0] = byte(dconn.sendFrameCounter >> 24 & 0xFF)
	FC[1] = byte(dconn.sendFrameCounter >> 16 & 0xFF)
	FC[2] = byte(dconn.sendFrameCounter >> 8 & 0xFF)
	FC[3] = byte(dconn.sendFrameCounter & 0xFF)

	// initialization vector
	IV := make([]byte, 12) // initialization vector
	if len(dconn.clientSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return err, nil
	}
	copy(IV, dconn.clientSystemTitle)
	copy(IV[len(dconn.clientSystemTitle):], FC)

	// additional authenticated data
	AAD := make([]byte, 1+len(dconn.AK))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)

	err, ciphertext, authTag := aesgcm(dconn.EK, IV, AAD, pdu, 0)
	if err != nil {
		return err, nil
	}

	length := 1 + len(FC) + len(ciphertext) + len(authTag)
	buf := new(bytes.Buffer)
	err = encodeAxdrLength(buf, uint16(length))
	if nil != err {
		return err, nil
	}
	LEN := buf.Bytes()

	epdu = make([]byte, 1+len(LEN)+1+len(FC)+len(ciphertext)+len(authTag))
	p := epdu[0:]
	p[0] = tag
	copy(p[1:], LEN)
	p = p[1+len(LEN):]
	p[0] = SC
	copy(p[1:], FC)
	copy(p[1+len(FC):], ciphertext)
	copy(p[1+len(FC)+len(ciphertext):], authTag)

	return nil, epdu
}

func (dconn *DlmsConn) decryptPduGSM(pdu []byte) (err error, dpdu []byte) {
	// tag
	err, _ = gloTagToCosemTag(pdu[0])
	if nil != err {
		return err, nil
	}

	// skip length
	buf := bytes.NewBuffer(pdu[1:])
	err, _ = decodeAxdrLength(buf)
	if nil != err {
		return err, nil
	}
	pdu = buf.Bytes()

	// security control
	SC := pdu[0] // security control
	if SC != 0x30 {
		err = fmt.Errorf("unexpected security control")
		errorLog("%s", err)
		return err, nil
	}

	// frame counter
	var frameCounter uint32
	frameCounter |= uint32(pdu[1]) << 24
	frameCounter |= uint32(pdu[2]) << 16
	frameCounter |= uint32(pdu[3]) << 8
	frameCounter |= uint32(pdu[4])
	FC := pdu[1:5]

	// initialization vector
	IV := make([]byte, 12) // initialization vector
	if len(dconn.serverSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return err, nil
	}
	copy(IV, dconn.serverSystemTitle)
	copy(IV[len(dconn.serverSystemTitle):], FC)

	// additional authenticated data
	AAD := make([]byte, 1+len(dconn.AK))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)

	ciphertext := pdu[1+4 : len(pdu)-GCM_TAG_LEN]
	receivedAuthTag := pdu[len(pdu)-GCM_TAG_LEN:]

	err, dpdu, authTag := aesgcm(dconn.EK, IV, AAD, ciphertext, 1)
	if err != nil {
		return err, nil
	}

	if len(authTag) != len(receivedAuthTag) {
		err = fmt.Errorf("unexpected authentication tag")
		errorLog("%s", err)
		return err, nil
	}
	for i := 0; i < len(receivedAuthTag); i++ {
		if authTag[i] != receivedAuthTag[i] {
			err = fmt.Errorf("unexpected authentication tag")
			errorLog("%s", err)
			return err, nil
		}
	}

	return nil, dpdu
}

func (dconn *DlmsConn) encryptPdu(pdu []byte) (err error, epdu []byte) {
	if dconn.authenticationMechanismId == high_level_security_mechanism_using_GMAC {
		err, epdu = dconn.encryptPduGSM(pdu)
		debugLog("encrypted app pdu: % 0X", epdu)
		return err, epdu
	} else if dconn.authenticationMechanismId == lowest_level_security_mechanism {
		return nil, pdu
	} else {
		err = fmt.Errorf("authentication mechanism %v not supported", dconn.authenticationMechanismId)
		errorLog("%s", err)
		return err, nil
	}
}

func (dconn *DlmsConn) decryptPdu(pdu []byte) (err error, dpdu []byte) {
	if dconn.authenticationMechanismId == high_level_security_mechanism_using_GMAC {
		err, dpdu = dconn.decryptPduGSM(pdu)
		debugLog("decrypted app pdu: % 0X", dpdu)
		return err, dpdu
	} else if dconn.authenticationMechanismId == lowest_level_security_mechanism {
		return nil, pdu
	} else {
		err = fmt.Errorf("authentication mechanism %v not supported", dconn.authenticationMechanismId)
		errorLog("%s", err)
		return err, nil
	}
}

func (aconn *AppConn) doChallengeClientSide_for_high_level_security_mechanism_using_GMAC() (err error) {
	dconn := aconn.dconn

	// return back to server encrypted StoC challenge to let server authenticate client first

	// security control
	SC := byte(0x30) // security control

	// frame counter
	dconn.sendFrameCounter += 1
	FC := make([]byte, 4)
	FC[0] = byte(dconn.sendFrameCounter >> 24 & 0xFF)
	FC[1] = byte(dconn.sendFrameCounter >> 16 & 0xFF)
	FC[2] = byte(dconn.sendFrameCounter >> 8 & 0xFF)
	FC[3] = byte(dconn.sendFrameCounter & 0xFF)

	// initialization vector
	IV := make([]byte, 12) // initialization vector
	if len(dconn.clientSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return err
	}
	copy(IV, dconn.clientSystemTitle)
	copy(IV[len(dconn.clientSystemTitle):], FC)

	// additional authenticated data
	AAD := make([]byte, 1+len(dconn.AK)+len(dconn.serverToClientChallenge))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)
	copy(AAD[1+len(dconn.AK):], dconn.serverToClientChallenge)

	err, _, authTag := aesgcm(dconn.EK, IV, AAD, []byte{}, 0)
	if err != nil {
		return err
	}

	data := make([]byte, 1+4+len(authTag))
	copy(data, []byte{SC})
	copy(data[1:], FC)
	copy(data[1+len(FC):], authTag)

	// do remote method call passing to server encrypted StoC as method input parameter

	debugLog("authenticating with server, sending  f(StoC): %0X", data)

	method := new(DlmsRequest)
	method.ClassId = 15
	method.InstanceId = &DlmsOid{0x00, 0x00, 0x28, 0x00, 0x00, 0xFF}
	method.MethodId = 1
	methodParameters := new(DlmsData)
	methodParameters.SetOctetString(data)
	method.MethodParameters = methodParameters
	methods := make([]*DlmsRequest, 1)
	methods[0] = method
	rep, err := aconn.SendRequest(methods)
	if nil != err {
		return err
	}
	if 0 != rep.ActionResultAt(0) {
		err = fmt.Errorf("server did not authenticate client: call to classId 15, instanceId {0,0,40,0,0,255}, methodId 1, failed: actionResult: %d\n", rep.ActionResultAt(0))
		errorLog("%s", err)
		return err
	}
	if rep.DataAt(0).Typ == DATA_TYPE_OCTET_STRING {
		data = rep.DataAt(0).GetOctetString()
		debugLog("client authenticated by server, received f(CtoS): %0X", data)
	} else {
		err = fmt.Errorf("server returned unexpected data type: %v", rep.DataAt(0).Typ)
		errorLog("%s", err)
		return err
	}

	debugLog("authenticating server ...")

	// security control
	SC = data[0]
	if SC != byte(0x30) {
		err = fmt.Errorf("server not authenticated by client, received wrong SC: %0X", SC)
		errorLog("%s", err)
		return err
	}

	// frame counter
	FC = data[1:5]
	frameCounter := uint32(0)
	frameCounter |= uint32(FC[0]) << 3
	frameCounter |= uint32(FC[1]) << 2
	frameCounter |= uint32(FC[2]) << 1
	frameCounter |= uint32(FC[3]) << 0

	// auth tag
	authTagReceived := data[5:]

	// initialization vector
	if len(dconn.serverSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return err
	}
	copy(IV, dconn.serverSystemTitle)
	copy(IV[len(dconn.serverSystemTitle):], FC)

	// additional authenticated data
	AAD = make([]byte, 1+len(dconn.AK)+len(dconn.clientToServerChallenge))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)
	copy(AAD[1+len(dconn.AK):], dconn.clientToServerChallenge)

	err, _, authTag = aesgcm(dconn.EK, IV, AAD, []byte{}, 1)
	if err != nil {
		return err
	}
	if len(authTagReceived) != len(authTag) {
		err = fmt.Errorf("did not authenticate server, authentication tag differs")
		errorLog("%s", err)
		return err
	}
	for i := 0; i < len(authTag); i++ {
		if authTagReceived[i] != authTag[i] {
			err = fmt.Errorf("did not authenticate server, authentication tag differs")
			errorLog("%s", err)
			return err
		}
	}

	debugLog("server authenticated")
	return nil
}

func (dconn *DlmsConn) AppConnectWithPassword(applicationClient uint16, logicalDevice uint16, invokeId uint8, password string) (aconn *AppConn, err error) {
	var aarq = AARQ{
		appCtxt:   LogicalName_NoCiphering,
		authMech:  LowLevelSecurity,
		authValue: password,
	}
	pdu, err := aarq.encode()
	if err != nil {
		return nil, err
	}

	err = dconn.transportSend(applicationClient, logicalDevice, pdu)
	if nil != err {
		return nil, err
	}
	pdu, err = dconn.transportReceive(logicalDevice, applicationClient)
	if nil != err {
		return nil, err
	}

	var aare AARE
	err = aare.decode(pdu)
	if err != nil {
		return nil, err
	}
	if aare.result != AssociationAccepted {
		err = fmt.Errorf("app connect failed, result: %v, diagnostic: %v", aare.result, aare.diagnostic)
		errorLog("%s", err)
		return nil, err
	} else {
		aconn = NewAppConn(dconn, applicationClient, logicalDevice, invokeId)
		return aconn, nil
	}

}

func (dconn *DlmsConn) AppConnectWithSecurity5(applicationClient uint16, logicalDevice uint16, invokeId uint8, authenticationKey []byte, encryptionKey []byte, applicationContextName []uint32, callingAPtitle []byte, clientToServerChallenge string, initiateRequest *DlmsInitiateRequest, sendFrameCounter uint32) (aconn *AppConn, initiateResponse *DlmsInitiateResponse, err error) {

	var buf *bytes.Buffer

	// encode and encrypt initiateRequest

	dconn.AK = authenticationKey
	dconn.EK = encryptionKey

	var userInformation []byte

	buf = new(bytes.Buffer)
	err = initiateRequest.encode(buf)
	if nil != err {
		return nil, nil, err
	}
	initiateRequestBytes := buf.Bytes()

	// enforce 128 bit keys
	if len(dconn.AK) != 16 {
		err = fmt.Errorf("authentication key length is not 16")
		errorLog("%s", err)
		return nil, nil, err
	}
	if len(dconn.EK) != 16 {
		err = fmt.Errorf("encryption key length is not 16")
		errorLog("%s", err)
		return nil, nil, err
	}

	// return back to server encrypted StoC challenge to let server authenticate client first

	// security control
	SC := byte(0x30) // security control

	// frame counter
	dconn.sendFrameCounter = sendFrameCounter + 1
	FC := make([]byte, 4)
	FC[0] = byte(dconn.sendFrameCounter >> 24 & 0xFF)
	FC[1] = byte(dconn.sendFrameCounter >> 16 & 0xFF)
	FC[2] = byte(dconn.sendFrameCounter >> 8 & 0xFF)
	FC[3] = byte(dconn.sendFrameCounter & 0xFF)

	dconn.clientSystemTitle = callingAPtitle

	// initialization vector
	IV := make([]byte, 12) // initialization vector
	if len(dconn.clientSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return nil, nil, err
	}
	copy(IV, dconn.clientSystemTitle)
	copy(IV[len(dconn.clientSystemTitle):], FC)

	// additional authenticated data
	AAD := make([]byte, 1+len(dconn.AK))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)

	debugLog("AppConnectWithSecurity5(): initiate request: % 0X", initiateRequestBytes)
	err, initiateRequestBytesEncrypted, authTag := aesgcm(dconn.EK, IV, AAD, initiateRequestBytes, 0)
	if err != nil {
		return nil, nil, err
	}
	debugLog("AppConnectWithSecurity5(): initiate request encrypted: % 0X", initiateRequestBytesEncrypted)

	length := 1 + 4 + len(initiateRequestBytesEncrypted) + len(authTag)
	buf = new(bytes.Buffer)
	err = encodeAxdrLength(buf, uint16(length))
	if nil != err {
		return nil, nil, err
	}
	LEN := buf.Bytes()

	userInformation = make([]byte, 1+len(LEN)+1+4+len(initiateRequestBytesEncrypted)+len(authTag)) // glo-initiateRequest tag + LEN + SC + frameCounter + code + authTag
	p := userInformation[0:]
	p[0] = 33 // glo-initiateRequest
	copy(p[1:], LEN)
	p = p[1+len(LEN):]
	p[0] = SC
	copy(p[1:], FC)
	copy(p[1+len(FC):], initiateRequestBytesEncrypted)
	copy(p[1+len(FC)+len(initiateRequestBytesEncrypted):], authTag)
	debugLog("AppConnectWithSecurity5(): AARQ.user_information: % 0X", userInformation)

	var aarq AARQapdu

	dconn.clientToServerChallenge = clientToServerChallenge

	aarq.applicationContextName = tAsn1ObjectIdentifier(applicationContextName)
	_callingAPtitle := tAsn1OctetString(callingAPtitle)
	aarq.callingAPtitle = &_callingAPtitle
	aarq.senderAcseRequirements = &tAsn1BitString{
		buf:        []byte{0x80}, // bit 0 == 1 => the authentication functional unit is selected
		bitsUnused: 7,
	}
	mechanismName := (tAsn1ObjectIdentifier)([]uint32{2, 16, 756, 5, 8, 2, 5})
	aarq.mechanismName = &mechanismName
	aarq.callingAuthenticationValue = new(tAsn1Choice)
	aarq.callingAuthenticationValue.setVal(0, tAsn1GraphicString([]byte(clientToServerChallenge)))

	_userInformation := tAsn1OctetString(userInformation)
	aarq.userInformation = &_userInformation

	buf = new(bytes.Buffer)
	err = encode_AARQapdu(buf, &aarq)
	if nil != err {
		return nil, nil, err
	}
	pdu := buf.Bytes()

	err = dconn.transportSend(applicationClient, logicalDevice, pdu)
	if nil != err {
		return nil, nil, err
	}
	pdu, err = dconn.transportReceive(logicalDevice, applicationClient)
	if nil != err {
		return nil, nil, err
	}

	buf = bytes.NewBuffer(pdu)
	err, aare := decode_AAREapdu(buf)
	if nil != err {
		return nil, nil, err
	}

	// verify AARE

	if aare.result != 0 {
		err = fmt.Errorf("app connect failed: verify AARE: result %v", aare.result)
		errorLog("%s", err)
		return nil, nil, err
	}
	if !(aare.resultSourceDiagnostic.tag == 1 && aare.resultSourceDiagnostic.val.(tAsn1Integer) == tAsn1Integer(14)) { // 14 - authentication-required
		err = fmt.Errorf("app connect failed: verify AARE: meter did not require authentication")
		errorLog("%s", err)
		return nil, nil, err
	}
	if aare.mechanismName == nil {
		err = fmt.Errorf("app connect failed: verify AARE: meter did not require expected authentication mechanism id: mechanism_id(5)")
		errorLog("%s", err)
		return nil, nil, err
	}
	oi := ([]uint32)(*aare.mechanismName)
	if !(oi[0] == 2 && oi[1] == 16 && oi[2] == 756 && oi[3] == 5 && oi[4] == 8 && oi[5] == 2 && oi[6] == 5) {
		err = fmt.Errorf("app connect failed: verify AARE: meter did not require expected authentication mechanism id: mechanism_id(5)")
		errorLog("%s", err)
		return nil, nil, err
	}
	if aare.respondingAPtitle == nil {
		err = fmt.Errorf("app connect failed: verify AARE: meter did not send respondingAPtitle")
		errorLog("%s", err)
		return nil, nil, err
	}

	dconn.serverSystemTitle = ([]byte)(*aare.respondingAPtitle)

	if aare.respondingAuthenticationValue.tag == 0 {
		dconn.serverToClientChallenge = string(aare.respondingAuthenticationValue.val.(tAsn1GraphicString))
	} else {
		err = fmt.Errorf("app connect failed: AARE: meter did not send client to server challenge")
		errorLog("%s", err)
		return nil, nil, err
	}

	// decrypt and decode initiateResponse

	userInformation = ([]byte)(*aare.userInformation)

	debugLog("AppConnectWithSecurity5(): AARE.user_information: % 0X", userInformation)

	// glo-initiateResponse [40] IMPLICIT OCTET STRING,
	if 40 != userInformation[0] {
		err = fmt.Errorf("wrong tag for initiateResponse")
		return nil, nil, err
	}

	// skip length
	buf = bytes.NewBuffer(userInformation[1:])
	err, _ = decodeAxdrLength(buf)
	if nil != err {
		return nil, nil, err
	}
	p = buf.Bytes()

	SC = p[0]
	if 0x30 != SC {
		err = fmt.Errorf("wrong tag for initiateResponse")
		return nil, nil, err
	}
	copy(FC, p[1:1+4])
	frameCounter := uint32(0)
	frameCounter |= uint32(FC[0]) << 3
	frameCounter |= uint32(FC[1]) << 2
	frameCounter |= uint32(FC[2]) << 1
	frameCounter |= uint32(FC[3]) << 0

	// initialization vector
	if len(dconn.serverSystemTitle) != 8 {
		err = fmt.Errorf("system title length is not 8")
		errorLog("%s", err)
		return nil, nil, err
	}
	copy(IV, dconn.serverSystemTitle)
	copy(IV[len(dconn.serverSystemTitle):], FC)

	// additional authenticated data
	AAD = make([]byte, 1+len(dconn.AK))
	AAD[0] = SC
	copy(AAD[1:], dconn.AK)

	initiateResponseBytesEncrypted := p[1+4 : len(p)-GCM_TAG_LEN]

	err, initiateResponseBytes, authTag := aesgcm(dconn.EK, IV, AAD, initiateResponseBytesEncrypted, 1)
	if err != nil {
		return nil, nil, err
	}
	receivedAuthTag := p[len(p)-GCM_TAG_LEN:]

	if len(authTag) != len(receivedAuthTag) {
		err = fmt.Errorf("unexpected authentication tag")
		errorLog("%s", err)
		return nil, nil, err
	}
	for i := 0; i < len(receivedAuthTag); i++ {
		if authTag[i] != receivedAuthTag[i] {
			err = fmt.Errorf("unexpected authentication tag")
			errorLog("%s", err)
			return nil, nil, err
		}
	}

	initiateResponse = new(DlmsInitiateResponse)
	err = initiateResponse.decode(bytes.NewReader(initiateResponseBytes))
	if nil != err {
		return nil, nil, err
	}

	aconn = NewAppConn(dconn, applicationClient, logicalDevice, invokeId)

	err = aconn.doChallengeClientSide_for_high_level_security_mechanism_using_GMAC()
	if nil != err {
		return nil, nil, err
	}
	dconn.authenticationMechanismId = high_level_security_mechanism_using_GMAC

	return aconn, initiateResponse, nil

}

func (dconn *DlmsConn) AppConnectRaw(applicationClient uint16, logicalDevice uint16, invokeId uint8, aarq []byte, aare []byte) (aconn *AppConn, err error) {
	err = dconn.transportSend(applicationClient, logicalDevice, aarq)
	if nil != err {
		return nil, err
	}
	pdu, err := dconn.transportReceive(logicalDevice, applicationClient)
	if nil != err {
		return nil, err
	}
	if !bytes.Equal(pdu, aare) {
		err = errors.New("received unexpected AARE")
		return nil, err
	} else {
		aconn = NewAppConn(dconn, applicationClient, logicalDevice, invokeId)
		return aconn, nil
	}
}

func (dconn *DlmsConn) AppConnect(applicationClient uint16, logicalDevice uint16, invokeId uint8, aarq *AARQapdu) (aconn *AppConn, aare *AAREapdu, err error) {
	var buf *bytes.Buffer

	buf = new(bytes.Buffer)
	err = encode_AARQapdu(buf, aarq)
	if nil != err {
		return nil, nil, err
	}
	aarqBytes := buf.Bytes()

	err = dconn.transportSend(applicationClient, logicalDevice, aarqBytes)
	if nil != err {
		return nil, nil, err
	}
	pdu, err := dconn.transportReceive(logicalDevice, applicationClient)
	if nil != err {
		return nil, nil, err
	}

	buf = bytes.NewBuffer(pdu)
	err, aare = decode_AAREapdu(buf)
	if nil != err {
		return nil, nil, err
	}

	if aare.result != 0 {
		err = fmt.Errorf("AARE.result: %v", aare.result)
		return nil, aare, err
	} else {
		aconn = NewAppConn(dconn, applicationClient, logicalDevice, invokeId)
		return aconn, aare, nil
	}
}

func TcpConnect(ipAddr string, port int) (dconn *DlmsConn, err error) {
	var (
		conn net.Conn
	)

	dconn = new(DlmsConn)
	dconn.transportType = Transport_TCP

	debugLog("connecting tcp transport: %s:%d\n", ipAddr, port)
	conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", ipAddr, port))
	if nil != err {
		return nil, err
	}
	dconn.rwc = conn

	debugLog("tcp transport connected: %s:%d\n", ipAddr, port)
	return dconn, nil

}

/*
'responseTimeout' should be set to network roundtrip time if hdlc is used over
    unreliable transport and it should be set to eternity hdlc is used
    over reliable tcp.
    This timeout is part of hdlc error recovery function in case of lost or delayed
    frames over unreliable transport. In case of hdlc over reliable tcp
    this 'responseTimeout' should be set to eterinty
    to avoid unnecessary sending of RR frames.

Optional 'cosemWaitTime' should be set to average time what it takes for
    cosem layer to generate request or reply. This should be used only if hdlc
    is used for cosem and it serves
    avoiding of sending unnecessary RR frames.
*/
func HdlcConnect(ipAddr string, port int, applicationClient uint16, logicalDevice uint16, physicalDevice *uint16, serverAddressLength *int, responseTimeout time.Duration, cosemWaitTime *time.Duration, snrmTimeout time.Duration, discTimeout time.Duration) (dconn *DlmsConn, err error) {
	var (
		conn net.Conn
	)

	dconn = new(DlmsConn)
	dconn.transportType = Transport_HDLC

	debugLog("connecting hdlc transport over tcp: %s:%d\n", ipAddr, port)
	conn, err = net.Dial("tcp", fmt.Sprintf("%s:%d", ipAddr, port))
	if nil != err {
		errorLog("net.Dial() failed: %v", err)
		return nil, err
	}
	dconn.hdlcRwc = conn

	client := NewHdlcTransport(dconn.hdlcRwc, responseTimeout, true, uint8(applicationClient), logicalDevice, physicalDevice, serverAddressLength)
	dconn.hdlcResponseTimeout = responseTimeout
	dconn.snrmTimeout = snrmTimeout
	dconn.discTimeout = discTimeout

	if nil != cosemWaitTime {
		client.SetForCosem(*cosemWaitTime)
	}

	// send SNRM
	ch := make(chan error, 1)
	go func() {
		ch <- client.SendDSNRM(nil, nil)
	}()
	select {
	case err = <-ch:
		if nil != err {
			errorLog("client.SendSNRM() failed: %v", err)
			conn.Close()
			client.Close()
			return nil, err
		}
		dconn.HdlcClient = client
		dconn.rwc = client
	case <-time.After(dconn.snrmTimeout):
		errorLog("SendSNRM(): error timeout")
		conn.Close()
		client.Close()
		return nil, ErrDlmsTimeout
	}

	return dconn, nil
}

func (dconn *DlmsConn) Close() (err error) {
	debugLog("closing transport connection ...")

	dconn.closedMutex.Lock()
	if !dconn.closed {
		switch dconn.transportType {
		case Transport_TCP:
			dconn.rwc.Close()
		case Transport_HDLC:
			// send DISC
			ch := make(chan error, 1)
			go func() {
				ch <- dconn.HdlcClient.SendDISC()
			}()
			select {
			case err = <-ch:
				if nil != err {
					errorLog("SendDISC() failed: %v", err)
				}
			case <-time.After(dconn.discTimeout):
				errorLog("SendDISC(): error timeout")
				err = ErrDlmsTimeout
			}
			dconn.hdlcRwc.Close()
			dconn.HdlcClient.Close()
		default:
			err = ErrUnknownTransport
		}
		dconn.closed = true
	}
	dconn.closedMutex.Unlock()
	debugLog("transport connection closed")
	return err
}
