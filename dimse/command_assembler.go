package dimse

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/giesekow/go-netdicom/pdu"
	"github.com/suyashkumar/dicom"
	"github.com/suyashkumar/dicom/pkg/tag"
)

// CommandAssembler is a helper that assembles a DIMSE command message and data
// payload from a sequence of P_DATA_TF PDUs.
type CommandAssembler struct {
	contextID      byte
	commandBytes   []byte
	command        Message
	dataBytes      []byte
	readAllCommand bool

	readAllData bool
}

func DecodeDIMSECommandMap(raw []byte) map[string]interface{} {
	result := make(map[string]interface{})
	reader := bytes.NewReader(raw)

	for reader.Len() > 0 {
		// Each DICOM command element in command set: tag(4 bytes) + length(4 bytes) + value
		var group, element uint16
		var length uint32

		// Read tag
		err := binary.Read(reader, binary.LittleEndian, &group)
		if err != nil {
			break
		}
		err = binary.Read(reader, binary.LittleEndian, &element)
		if err != nil {
			break
		}

		// Read length
		err = binary.Read(reader, binary.LittleEndian, &length)
		if err != nil {
			break
		}

		// Read value
		val := make([]byte, length)
		n, _ := reader.Read(val)
		if n != int(length) {
			break
		}

		tagStr := fmt.Sprintf("(%04X,%04X)", group, element)

		// Decode some known tags
		switch tagStr {
		case "(0000,0002)": // Affected SOP Class UID
			result["SOPClassUID"] = string(val)
		case "(0000,0100)": // Command Field
			if len(val) >= 2 {
				result["CommandField"] = binary.LittleEndian.Uint16(val[:2])
			}
		case "(0000,0110)": // Message ID
			if len(val) >= 2 {
				result["MessageID"] = binary.LittleEndian.Uint16(val[:2])
			}
		case "(0000,0120)": // Message ID Being Responded To
			if len(val) >= 2 {
				result["MessageIDBeingRespondedTo"] = binary.LittleEndian.Uint16(val[:2])
			}
		case "(0000,0200)": // Data Set Type
			if len(val) >= 2 {
				result["DataSetType"] = binary.LittleEndian.Uint16(val[:2])
			}
		case "(0000,0800)":
			if len(val) >= 2 {
				result["Priority"] = binary.LittleEndian.Uint16(val[:2])
			}

		default:
			result[tagStr] = val // raw bytes for unknown tags
		}
	}

	return result
}

// AddDataPDU is to be called for each P_DATA_TF PDU received from the
// network. If the fragment is marked as the last one, AddDataPDU returns
// <SOPUID, TransferSyntaxUID, payload, nil>.  If it needs more fragments, it
// returns <"", "", nil, nil>.  On error, it returns a non-nil error.
func (commandAssembler *CommandAssembler) AddDataPDU(pdu *pdu.PDataTf) (byte, Message, []byte, error) {
	for _, item := range pdu.Items {
		if commandAssembler.contextID == 0 {
			commandAssembler.contextID = item.ContextID
		} else if commandAssembler.contextID != item.ContextID {
			return 0, nil, nil, fmt.Errorf("mixed context: %d %d", commandAssembler.contextID, item.ContextID)
		}
		if item.Command {
			commandAssembler.commandBytes = append(commandAssembler.commandBytes, item.Value...)
			if item.Last {
				if commandAssembler.readAllCommand {
					return 0, nil, nil, fmt.Errorf("P_DATA_TF: found >1 command chunks with the Last bit set")
				}
				commandAssembler.readAllCommand = true
			}
		} else {
			commandAssembler.dataBytes = append(commandAssembler.dataBytes, item.Value...)
			if item.Last {
				if commandAssembler.readAllData {
					return 0, nil, nil, fmt.Errorf("P_DATA_TF: found >1 data chunks with the Last bit set")
				}
				commandAssembler.readAllData = true
			}
		}
	}
	if !commandAssembler.readAllCommand {
		return 0, nil, nil, nil
	}
	if commandAssembler.command == nil {
		bytesLen := len(commandAssembler.commandBytes)
		var parser dicom.Dataset
		var err error = nil
		if bytesLen < 100 {
			data := DecodeDIMSECommandMap(commandAssembler.commandBytes)
			messageId, msgid_ok := data["MessageID"]
			commandField, cmd_ok := data["CommandField"]
			priority, pr_ok := data["Priority"]

			if cmd_ok && int(commandField.(uint16)) == 48 {

				e1, _ := dicom.NewElement(tag.Tag{Group: 0x0000, Element: 0x0100}, []int{int(48)})
				e2, _ := dicom.NewElement(tag.Tag{Group: 0x0000, Element: 0x0110}, []int{int(1)})
				e3, _ := dicom.NewElement(tag.Tag{Group: 0x0000, Element: 0x0800}, []int{int(257)})

				if msgid_ok {
					e2, _ = dicom.NewElement(tag.Tag{Group: 0x0000, Element: 0x0110}, []int{int(messageId.(uint16))})
				}

				if pr_ok {
					e3, _ = dicom.NewElement(tag.Tag{Group: 0x0000, Element: 0x0800}, []int{int(priority.(uint16))})
				}

				parser = dicom.Dataset{
					Elements: []*dicom.Element{e1, e2, e3},
				}
			} else {
				parser = dicom.Dataset{}
			}

		} else {
			ioReader := bytes.NewReader(commandAssembler.commandBytes)
			parser, err = dicom.Parse(ioReader, int64(ioReader.Len()), nil, dicom.SkipPixelData(), dicom.SkipMetadataReadOnNewParserInit())
		}

		if err != nil {
			return 0, nil, nil, fmt.Errorf("P_DATA_TF: failed to parse command bytes: %w", err)
		}
		commandAssembler.command, err = ReadMessage(&parser)
		if err != nil {
			return 0, nil, nil, err
		}
	}
	if commandAssembler.command.HasData() && !commandAssembler.readAllData {
		return 0, nil, nil, nil
	}
	contextID := commandAssembler.contextID
	command := commandAssembler.command
	dataBytes := commandAssembler.dataBytes
	*commandAssembler = CommandAssembler{}
	return contextID, command, dataBytes, nil
	// TODO(saito) Verify that there's no unread items after the last command&data.
}
