package platform

type Header struct {
	Seq             int64
	SenderComponent string
	SenderId        string
	SenderSeq       int64
}

type Event struct {
	Header  Header
	Payload any // immutable and opaque
}
