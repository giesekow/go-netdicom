package dimse

import (
	"fmt"

	"github.com/giesekow/go-netdicom/commandset"
	"github.com/suyashkumar/dicom"
	dicomtag "github.com/suyashkumar/dicom/pkg/tag"
)

// Helper class for extracting values from a list of DicomElement.
type MessageDecoder struct {
	elements map[dicomtag.Tag]*dicom.Element
}

type isOptionalElement int

const (
	RequiredElement isOptionalElement = iota
	OptionalElement
)

type CommandDataSetType uint16

const (
	// CommandDataSetTypeNull indicates that the DIMSE message has no data payload,
	// when set in dicom.TagCommandDataSetType. Any other value indicates the
	// existence of a payload.
	CommandDataSetTypeNull CommandDataSetType = 0x101

	// CommandDataSetTypeNonNull indicates that the DIMSE message has a data
	// payload, when set in dicom.TagCommandDataSetType.
	CommandDataSetTypeNonNull CommandDataSetType = 1
)

func (d *MessageDecoder) Decode(commandField uint16) (Message, error) {
	switch commandField {
	case CommandFieldCStoreRq:
		return CStoreRq{}.decode(d)
	case CommandFieldCStoreRsp:
		return CStoreRsp{}.decode(d)
	case CommandFieldCFindRq:
		return CFindRq{}.decode(d)
	case CommandFieldCFindRsp:
		return CFindRsp{}.decode(d)
	case CommandFieldCGetRq:
		return CGetRq{}.decode(d)
	case CommandFieldCGetRsp:
		return CGetRsp{}.decode(d)
	case CommandFieldCMoveRq:
		return CMoveRq{}.decode(d)
	case CommandFieldCMoveRsp:
		return CMoveRsp{}.decode(d)
	case CommandFieldCEchoRq:
		return CEchoRq{}.decode(d)
	case CommandFieldCEchoRsp:
		return CEchoRsp{}.decode(d)
	default:
		return nil, fmt.Errorf("unknown DIMSE command 0x%x", commandField)
	}
}

func (d *MessageDecoder) UnparsedElements() []*dicom.Element {
	elems := make([]*dicom.Element, 0, len(d.elements))
	for _, elem := range d.elements {
		elems = append(elems, elem)
	}
	return elems
}

func (d *MessageDecoder) GetStatus() (s Status, err error) {
	statusCode, err := d.GetUInt16(commandset.Status, RequiredElement)
	if err != nil {
		return s, fmt.Errorf("GetStatus: failed to get status code: %w", err)
	}
	s.Status = StatusCode(statusCode)
	s.ErrorComment, err = d.GetString(commandset.ErrorComment, OptionalElement)
	if err != nil {
		return s, fmt.Errorf("GetStatus: failed to get error comment: %w", err)
	}
	return s, nil
}

func (d *MessageDecoder) GetCommandDataSetType() (CommandDataSetType, error) {
	cmdDataSetType, err := d.GetUInt16(commandset.CommandDataSetType, RequiredElement)
	if err != nil {
		return CommandDataSetTypeNull, fmt.Errorf("GetCommandDataSetType: failed to get command data set type: %w", err)
	}
	return CommandDataSetType(cmdDataSetType), nil
}

func (d *MessageDecoder) GetString(tag dicomtag.Tag, optional isOptionalElement) (string, error) {
	elem := d.elements[tag]
	if elem == nil {
		if optional == RequiredElement {
			return "", fmt.Errorf("GetString: tag %s not found", tag.String())
		}
		return "", nil
	}
	if elem.Value == nil {
		return "", fmt.Errorf("GetString: tag %s has no value", tag.String())
	}
	rawValue := elem.Value.GetValue()
	if rawValue == nil {
		return "", fmt.Errorf("GetString: tag %s has a nil value", tag.String())
	}
	v, ok := rawValue.([]string)
	if !ok {
		return "", fmt.Errorf("GetString: failed to convert tag %s to []string, got %d", tag.String(), elem.Value.ValueType())
	}
	if len(v) == 0 {
		return "", nil
	}
	delete(d.elements, tag)
	return v[0], nil
}

// Find an element with "tag", and extract a uint16 from it. Errors are reported in d.err.
func (d *MessageDecoder) GetUInt16(tag dicomtag.Tag, optional isOptionalElement) (uint16, error) {
	elem := d.elements[tag]
	if elem == nil {
		if optional == RequiredElement {
			return 0, fmt.Errorf("GetUInt16: tag %s not found", tag.String())
		}
		return 0, nil
	}
	if elem.Value == nil {
		return 0, fmt.Errorf("GetUInt16: tag %s has no value", tag.String())
	}
	if elem.Value.ValueType() != dicom.Ints {
		return 0, fmt.Errorf("GetUInt16: element %s is not an int, got %v", tag.String(), elem.Value.ValueType())
	}
	rawValue := elem.Value.GetValue()
	if rawValue == nil {
		return 0, fmt.Errorf("GetUInt16: tag %s has a nil value", tag.String())
	}
	v, ok := rawValue.([]int)
	if !ok {
		return 0, fmt.Errorf("GetUInt16: failed to convert tag %s to []int", tag.String())
	}
	if len(v) == 0 {
		return 0, nil
	}
	if v[0] < 0 || v[0] > 65535 {
		return 0, fmt.Errorf("GetUInt16: value %v is out of range for uint16", v)
	}
	delete(d.elements, tag)
	return uint16(v[0]), nil
}
