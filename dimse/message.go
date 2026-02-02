package dimse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/giesekow/go-netdicom/commandset"
	"github.com/suyashkumar/dicom"
	dicomtag "github.com/suyashkumar/dicom/pkg/tag"
)

// Message defines the common interface for all DIMSE message types.
type Message interface {
	fmt.Stringer // Print human-readable description for debugging.
	Encode(io.Writer) error
	// GetMessageID extracts the message ID field.
	GetMessageID() MessageID
	// CommandField returns the command field value of this message.
	CommandField() uint16
	// GetStatus returns the the response status value. It is nil for request message
	// types, and non-nil for response message types.
	GetStatus() *Status
	// HasData is true if we expect P_DATA_TF packets after the command packets.
	HasData() bool
}

const (
	CommandFieldAssocRq   uint16 = 0x0000 // Here only for assoc request response
	CommandFieldCStoreRq  uint16 = 0x0001
	CommandFieldCStoreRsp uint16 = 0x8001
	CommandFieldCFindRq   uint16 = 0x0020
	CommandFieldCFindRsp  uint16 = 0x8020
	CommandFieldCGetRq    uint16 = 0x0010
	CommandFieldCGetRsp   uint16 = 0x8010
	CommandFieldCMoveRq   uint16 = 0x0021
	CommandFieldCMoveRsp  uint16 = 0x8021
	CommandFieldCEchoRq   uint16 = 0x0030
	CommandFieldCEchoRsp  uint16 = 0x8030
)

type MessageID = uint16

func ReadMessage(dataset *dicom.Dataset) (message Message, err error) {
	mDecoder := MessageDecoder{
		elements: make(map[dicomtag.Tag]*dicom.Element),
	}
	for _, elem := range dataset.Elements {
		tag := elem.Tag
		mDecoder.elements[tag] = elem
	}
	commandField, err := mDecoder.GetUInt16(commandset.CommandField, RequiredElement)
	if err != nil {
		return nil, fmt.Errorf("ReadMessage: failed to get command field: %w", err)
	}
	return mDecoder.Decode(commandField)
}

// EncodeMessage serializes the given message. Errors are reported through e.Error()
func EncodeMessage(out io.Writer, v Message) error {
	writer, err := dicom.NewWriter(out)
	if err != nil {
		return fmt.Errorf("EncodeMessage: error creating writer: %w", err)
	}
	subEncoderBuffer := bytes.Buffer{}
	if err := v.Encode(&subEncoderBuffer); err != nil {
		return fmt.Errorf("EncodeMessage: error encoding message: %w", err)
	}
	// DIMSE messages are always encoded Implicit+LE. See P3.7 6.3.1.
	writer.SetTransferSyntax(binary.LittleEndian, true)
	element, err := NewElement(commandset.CommandGroupLength, subEncoderBuffer.Len())
	if err != nil {
		return fmt.Errorf("EncodeMessage: failed to create CommandGroupLength element: %w", err)
	}
	writer.WriteElement(element)
	out.Write(subEncoderBuffer.Bytes())
	return nil
}
