//go:generate stringer -type StatusCode
package dimse

import (
	"fmt"

	"github.com/giesekow/go-netdicom/commandset"
	"github.com/suyashkumar/dicom"
)

// Status represents a result of a DIMSE call.  P3.7 C defines list of status
// codes and error payloads.
type Status struct {
	// Status==StatusSuccess on success. A non-zero value on error.
	Status StatusCode

	// Optional error payloads.
	ErrorComment string // Encoded as (0000,0902)
}

// Success is an OK status for a call.
var Success = Status{Status: StatusSuccess}

// StatusCode represents a DIMSE service response code, as defined in P3.7
type StatusCode uint16

const (
	StatusSuccess               StatusCode = 0
	StatusCancel                StatusCode = 0xFE00
	StatusSOPClassNotSupported  StatusCode = 0x0112
	StatusInvalidArgumentValue  StatusCode = 0x0115
	StatusInvalidAttributeValue StatusCode = 0x0106
	StatusInvalidObjectInstance StatusCode = 0x0117
	StatusUnrecognizedOperation StatusCode = 0x0211
	StatusNotAuthorized         StatusCode = 0x0124
	StatusPending               StatusCode = 0xff00

	// C-STORE-specific status codes. P3.4 GG4-1
	CStoreOutOfResources              StatusCode = 0xa700
	CStoreCannotUnderstand            StatusCode = 0xc000
	CStoreDataSetDoesNotMatchSOPClass StatusCode = 0xa900

	// C-FIND-specific status codes.
	CFindUnableToProcess StatusCode = 0xc000

	// C-MOVE/C-GET-specific status codes.
	CMoveOutOfResourcesUnableToCalculateNumberOfMatches StatusCode = 0xa701
	CMoveOutOfResourcesUnableToPerformSubOperations     StatusCode = 0xa702
	CMoveMoveDestinationUnknown                         StatusCode = 0xa801
	CMoveDataSetDoesNotMatchSOPClass                    StatusCode = 0xa900

	// Warning codes.
	StatusAttributeValueOutOfRange StatusCode = 0x0116
	StatusAttributeListError       StatusCode = 0x0107
)

func (s *Status) ToElements() ([]*dicom.Element, error) {
	statusElement, err := NewElement(commandset.Status, int(s.Status))
	if err != nil {
		return nil, fmt.Errorf("Status.ToElements: error creating status element with status %v: %w", s.Status, err)
	}
	elems := []*dicom.Element{statusElement}
	if s.ErrorComment != "" {
		errorCommentElement, err := NewElement(commandset.ErrorComment, s.ErrorComment)
		if err != nil {
			return nil, fmt.Errorf("Status.ToElements: error creating error comment element with comment %v: %w", s.ErrorComment, err)
		}
		elems = append(elems, errorCommentElement)
	}
	return elems, nil
}
