set -x
#go test -run TestAsn1
#go test -run TestAPDU
go test -run TestData
go test -run TestDlms
go test -run TestApp
go test -run TestHdlc
go test -run TestMeterTcp
go test -run TestMeterHdlc
