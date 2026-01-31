package pdu

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/giesekow/go-netdicom/pdu/pdu_item"
	"github.com/suyashkumar/dicom/pkg/dicomio"
)

// Defines A_ASSOCIATE_AC. P3.8 9.3.2 and 9.3.3
type AAssociateRQ struct {
	ProtocolVersion uint16
	// Reserved uint16
	CalledAETitle  string // For .._AC, the value is copied from A_ASSOCIATE_RQ
	CallingAETitle string // For .._AC, the value is copied from A_ASSOCIATE_RQ
	Items          []pdu_item.SubItem
}

func (AAssociateRQ) Read(d *dicomio.Reader) (PDU, error) {
	pdu := &AAssociateRQ{}
	var err error
	pdu.ProtocolVersion, err = d.ReadUInt16()
	if err != nil {
		return nil, err
	}
	d.Skip(2) // Reserved
	pdu.CalledAETitle, err = d.ReadString(16)
	if err != nil {
		return nil, err
	}
	pdu.CallingAETitle, err = d.ReadString(16)
	if err != nil {
		return nil, err
	}
	d.Skip(8 * 4)
	for !d.IsLimitExhausted() {
		item, err := pdu_item.DecodeSubItem(d)
		if err != nil {
			break
		}
		pdu.Items = append(pdu.Items, item)
	}
	if pdu.CalledAETitle == "" || pdu.CallingAETitle == "" {
		err = fmt.Errorf("A_ASSOCIATE.{Called,Calling}AETitle must not be empty, in %v", pdu.String())
	}
	return pdu, err
}

func (pdu *AAssociateRQ) Write() ([]byte, error) {
	var buf bytes.Buffer
	e := dicomio.NewWriter(&buf, binary.BigEndian, false)
	if pdu.CalledAETitle == "" || pdu.CallingAETitle == "" {
		return nil, fmt.Errorf("CalledAETitle or CallingAETitle cannot be empty: %+v", *pdu)
	}
	if err := e.WriteUInt16(pdu.ProtocolVersion); err != nil {
		return nil, err
	}
	if err := e.WriteZeros(2); err != nil {
		return nil, err
	}
	if err := e.WriteString(fillString(pdu.CalledAETitle)); err != nil {
		return nil, err
	}
	if err := e.WriteString(fillString(pdu.CallingAETitle)); err != nil {
		return nil, err
	}
	if err := e.WriteZeros(8 * 4); err != nil {
		return nil, err
	}
	for _, item := range pdu.Items {
		err := item.Write(e)
		if err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func (pdu *AAssociateRQ) String() string {
	return fmt.Sprintf("A_ASSOCIATE_AC{version:%v called:'%v' calling:'%v' items:%s}",
		pdu.ProtocolVersion,
		pdu.CalledAETitle, pdu.CallingAETitle, pdu_item.SubItemListString(pdu.Items))
}
