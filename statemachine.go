package netdicom

// Implements the network statemachine, as defined in P3.8 9.2.3.
// http://dicom.nema.org/medical/dicom/current/output/pdf/part08.pdf

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/giesekow/go-netdicom/dimse"
	"github.com/giesekow/go-netdicom/pdu"
	"github.com/giesekow/go-netdicom/pdu/pdu_item"
	"github.com/grailbio/go-dicom/dicomlog"
	"github.com/grailbio/go-dicom/dicomuid"
)

type stateType int

const (
	sta01 stateType = iota + 1
	sta02
	sta03
	sta04
	sta05
	sta06
	sta07
	sta08
	sta09
	sta10
	sta11
	sta12
	sta13
)

var stateDescriptions = map[stateType]string{
	sta01: "Idle",
	sta02: "Transport connection open (Awaiting A-ASSOCIATE-RQ PDU)",
	sta03: "Awaiting local A-ASSOCIATE response primitive (from local user)",
	sta04: "Awaiting transport connection opening to complete (from local transport service)",
	sta05: "Awaiting A-ASSOCIATE-AC or A-ASSOCIATE-RJ PDU",
	sta06: "Association established and ready for data transfer",
	sta07: "Awaiting A-RELEASE-RP PDU",
	sta08: "Awaiting local A-RELEASE response primitive (from local user)",
	sta09: "Release collision requestor side; awaiting A-RELEASE response (from local user)",
	sta10: "Release collision acceptor side; awaiting A-RELEASE-RP PDU",
	sta11: "Release collision requestor side; awaiting A-RELEASE-RP PDU",
	sta12: "Release collision acceptor side; awaiting A-RELEASE response primitive (from local user)",
	sta13: "Awaiting Transport Connection Close Indication (Association no longer exists)",
}

func (s *stateType) String() string {
	description, ok := stateDescriptions[*s]
	if !ok {
		description = "Unknown state"
	}
	return fmt.Sprintf("sta%02d(%s)", *s, description)
}

type eventType int

const (
	evt01 eventType = iota + 1
	evt02
	evt03
	evt04
	evt05
	evt06
	evt07
	evt08
	evt09
	evt10
	evt11
	evt12
	evt13
	evt14
	evt15
	evt16
	evt17
	evt18
	evt19
)

var eventDescriptions = map[eventType]string{
	evt01: "A-ASSOCIATE request (local user)",
	evt02: "Connection established (for service user)",
	evt03: "A-ASSOCIATE-AC PDU (received on transport connection)",
	evt04: "A-ASSOCIATE-RJ PDU (received on transport connection)",
	evt05: "Connection accepted (for service provider)",
	evt06: "A-ASSOCIATE-RQ PDU (on tranport connection)",
	evt07: "A-ASSOCIATE response primitive (accept)",
	evt08: "A-ASSOCIATE response primitive (reject)",
	evt09: "P-DATA request primitive",
	evt10: "P-DATA-TF PDU (on transport connection)",
	evt11: "A-RELEASE request primitive",
	evt12: "A-RELEASE-RQ PDU (on transport)",
	evt13: "A-RELEASE-RP PDU (on transport)",
	evt14: "A-RELEASE response primitive",
	evt15: "A-ABORT request primitive",
	evt16: "A-ABORT PDU (on transport)",
	evt17: "Transport connection closed indication (local transport service)",
	evt18: "ARTIM timer expired (Association reject/release timer)",
	evt19: "Unrecognized or invalid PDU received",
}

func (e *eventType) String() string {
	description, ok := eventDescriptions[*e]
	if !ok {
		panic(fmt.Sprintf("dicom.stateMachine: Unknown event type %v", int(*e)))
	}
	return fmt.Sprintf("evt%02d(%s)", *e, description)
}

type stateAction struct {
	Name        string
	Description string
	Callback    func(sm *stateMachine, event stateEvent) stateType
}

func (s *stateAction) String() string {
	return fmt.Sprintf("%s(%s)", s.Name, s.Description)
}

var actionAe1 = &stateAction{"AE-1",
	"Issue TRANSPORT CONNECT request primitive to local transport service",
	func(sm *stateMachine, event stateEvent) stateType {
		// Nothing to do now. We expect ServiceUser to dial a connection and emit either
		// evt02 (on success) or evt17 (on failure)
		return sta04
	}}

var actionAe2 = &stateAction{"AE-2", "Connection established on the user side. Send A-ASSOCIATE-RQ-PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		doassert(event.conn != nil)
		sm.conn = event.conn
		go networkReaderThread(sm.netCh, event.conn, DefaultMaxPDUSize, sm.label)
		items := sm.contextManager.generateAssociateRequest(
			sm.userParams.SOPClasses,
			sm.userParams.TransferSyntaxes)
		pdu := &pdu.AAssociateRQ{
			ProtocolVersion: pdu.CurrentProtocolVersion,
			CalledAETitle:   sm.userParams.CalledAETitle,
			CallingAETitle:  sm.userParams.CallingAETitle,
			Items:           items,
		}
		sendPDU(sm, pdu)
		sm.startTimer()
		return sta05
	}}

var actionAe3 = &stateAction{"AE-3", "Issue A-ASSOCIATE confirmation (accept) primitive",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.stopTimer()
		v := event.pdu.(*pdu.AAssociateAC)
		err := sm.contextManager.onAssociateResponse(v.Items)
		if err == nil {
			sm.upcallCh <- upcallEvent{
				eventType: upcallEventHandshakeCompleted,
				cm:        sm.contextManager,
			}
			return sta06
		}
		dicomlog.Vprintf(0, "dicom.stateMachine: AE-3: %v", err)
		return actionAa8.Callback(sm, event)
	}}

var actionAe4 = &stateAction{"AE-4", "Issue A-ASSOCIATE confirmation (reject) primitive and close transport connection",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.closeConnection()
		return sta01
	}}

var actionAe5 = &stateAction{"AE-5", "Issue Transport connection response primitive; start ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		doassert(event.conn != nil)
		sm.startTimer()
		go func(ch chan stateEvent, conn net.Conn) {
			networkReaderThread(ch, conn, DefaultMaxPDUSize, sm.label)
		}(sm.netCh, event.conn)
		return sta02
	}}

func extractPresentationContextItems(items []pdu_item.SubItem) []*pdu_item.PresentationContextItem {
	var contextItems []*pdu_item.PresentationContextItem
	for _, item := range items {
		if n, ok := item.(*pdu_item.PresentationContextItem); ok {
			contextItems = append(contextItems, n)
		}
	}
	return contextItems
}

var actionAe6 = &stateAction{"AE-6", `Stop ARTIM timer and if A-ASSOCIATE-RQ acceptable by "
service-dul: issue A-ASSOCIATE indication primitive
otherwise issue A-ASSOCIATE-RJ-PDU and start ARTIM timer`,
	func(sm *stateMachine, event stateEvent) stateType {
		sm.stopTimer()
		v := event.pdu.(*pdu.AAssociateRQ)
		if v.ProtocolVersion != 0x0001 {
			dicomlog.Vprintf(0, "dicom.stateMachine(%s): Wrong remote protocol version 0x%x", sm.label, v.ProtocolVersion)
			rj := pdu.AAssociateRj{Result: 1, Source: 2, Reason: 2}
			sendPDU(sm, &rj)
			sm.startTimer()
			return sta13
		}
		responses, err := sm.contextManager.onAssociateRequest(v.Items)
		if err != nil {
			// TODO(saito) set proper error code.
			sm.downcallCh <- stateEvent{
				event: evt08,
				pdu: &pdu.AAssociateRj{
					Result: pdu.ResultRejectedPermanent,
					Source: pdu.SourceULServiceProviderACSE,
					Reason: 1,
				},
			}
		} else {
			doassert(len(responses) > 0)
			doassert(v.CalledAETitle != "")
			doassert(v.CallingAETitle != "")
			sm.downcallCh <- stateEvent{
				event: evt07,
				pdu: &pdu.AAssociateAC{
					ProtocolVersion: pdu.CurrentProtocolVersion,
					CalledAETitle:   v.CalledAETitle,
					CallingAETitle:  v.CallingAETitle,
					Items:           responses,
				},
			}
		}
		return sta03
	}}
var actionAe7 = &stateAction{"AE-7", "Send A-ASSOCIATE-AC PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, event.pdu.(*pdu.AAssociateAC))
		sm.upcallCh <- upcallEvent{
			eventType: upcallEventHandshakeCompleted,
			cm:        sm.contextManager,
		}
		return sta06
	}}

var actionAe8 = &stateAction{"AE-8", "Send A-ASSOCIATE-RJ PDU and start ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, event.pdu.(*pdu.AAssociateRj))
		sm.startTimer()
		return sta13
	}}

// Produce a list of P_DATA_TF PDUs that collective store "data".
func splitDataIntoPDUs(sm *stateMachine, abstractSyntaxName string, command bool, data []byte) []pdu.PDataTf {
	doassert(len(data) > 0)
	context, err := sm.contextManager.lookupByAbstractSyntaxUID(abstractSyntaxName)
	if err != nil {
		// TODO(saito) Don't crash here.
		panic(fmt.Sprintf("dicom.stateMachine(%s): Illegal syntax name %s: %s", sm.label, dicomuid.UIDString(abstractSyntaxName), err))
	}
	var pdus []pdu.PDataTf
	// two byte header overhead.
	//
	// TODO(saito) move the magic number elsewhere.
	var maxChunkSize = sm.contextManager.peerMaxPDUSize - 8
	if maxChunkSize <= 0 {
		panic(fmt.Sprintf("dicom.stateMachine(%s): Invalid max PDU size %d", sm.label, sm.contextManager.peerMaxPDUSize))
	}
	for len(data) > 0 {
		chunkSize := len(data)
		if chunkSize > maxChunkSize {
			chunkSize = maxChunkSize
		}
		chunk := data[0:chunkSize]
		data = data[chunkSize:]
		pdus = append(pdus, pdu.PDataTf{Items: []pdu.PresentationDataValueItem{
			pdu.PresentationDataValueItem{
				ContextID: context.contextID,
				Command:   command,
				Last:      false, // Set later.
				Value:     chunk,
			}}})
	}
	if len(pdus) > 0 {
		pdus[len(pdus)-1].Items[0].Last = true
	}
	return pdus
}

// Data transfer related actions
var actionDt1 = &stateAction{"DT-1", "Send P-DATA-TF PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		doassert(event.dimsePayload != nil)
		command := event.dimsePayload.command
		doassert(command != nil)
		e := bytes.Buffer{}
		err := dimse.EncodeMessage(&e, command)
		if err != nil {
			panic(fmt.Sprintf("Failed to encode DIMSE cmd %v: %v", command, err))
		}
		dicomlog.Vprintf(1, "dicom.stateMachine(%s): Send DIMSE msg: %v", sm.label, command)
		pdus := splitDataIntoPDUs(sm, event.dimsePayload.abstractSyntaxName, true /*command*/, e.Bytes())
		for _, pdu := range pdus {
			sendPDU(sm, &pdu)
		}
		if command.HasData() {
			dicomlog.Vprintf(1, "dicom.stateMachine(%s): Send DIMSE data of %db, command: %v", sm.label, len(event.dimsePayload.data), command)
			pdus := splitDataIntoPDUs(sm, event.dimsePayload.abstractSyntaxName, false /*data*/, event.dimsePayload.data)
			for _, pdu := range pdus {
				sendPDU(sm, &pdu)
			}
		} else if len(event.dimsePayload.data) > 0 {
			panic(fmt.Sprintf("dicom.stateMachine(%s): Found DIMSE data of %db, command: %v", sm.label, len(event.dimsePayload.data), command))
		}
		return sta06
	}}

var actionDt2 = &stateAction{"DT-2", "Send P-DATA indication primitive",
	func(sm *stateMachine, event stateEvent) stateType {
		contextID, command, data, err := sm.commandAssembler.AddDataPDU(event.pdu.(*pdu.PDataTf))
		if err == nil {
			if command != nil { // All fragments received
				dicomlog.Vprintf(1, "dicom.stateMachine(%s): DIMSE request: %v", sm.label, command)
				sm.upcallCh <- upcallEvent{
					eventType: upcallEventData,
					cm:        sm.contextManager,
					contextID: contextID,
					command:   command,
					data:      data}
			}
			return sta06
		}
		dicomlog.Vprintf(0, "dicom.stateMachine(%s): Failed to assemble data: %v", sm.label, err) // TODO(saito)
		return actionAa8.Callback(sm, event)
	}}

// Assocation Release related actions
var actionAr1 = &stateAction{"AR-1", "Send A-RELEASE-RQ PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AReleaseRq{})
		return sta07
	}}
var actionAr2 = &stateAction{"AR-2", "Issue A-RELEASE indication primitive",
	func(sm *stateMachine, event stateEvent) stateType {
		// TODO(saito) Do RELEASE callback here.
		sm.downcallCh <- stateEvent{event: evt14}
		return sta08
	}}

var actionAr3 = &stateAction{"AR-3", "Issue A-RELEASE confirmation primitive and close transport connection",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AReleaseRp{})
		sm.closeConnection()
		return sta01
	}}
var actionAr4 = &stateAction{"AR-4", "Issue A-RELEASE-RP PDU and start ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AReleaseRp{})
		sm.startTimer()
		return sta13
	}}

var actionAr5 = &stateAction{"AR-5", "Stop ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.stopTimer()
		return sta01
	}}

var actionAr6 = &stateAction{"AR-6", "Issue P-DATA indication",
	func(sm *stateMachine, event stateEvent) stateType {
		return sta07
	}}

var actionAr7 = &stateAction{"AR-7", "Issue P-DATA-TF PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		doassert(event.dimsePayload != nil)
		command := event.dimsePayload.command
		doassert(command != nil)
		e := bytes.Buffer{}
		err := dimse.EncodeMessage(&e, command)
		if err != nil {
			panic(fmt.Sprintf("dicom.StateMachine %s: Failed to encode DIMSE cmd %v: %v", sm.label, command, err))
		}
		pdus := splitDataIntoPDUs(sm, event.dimsePayload.abstractSyntaxName, true /*command*/, e.Bytes())
		for _, pdu := range pdus {
			sendPDU(sm, &pdu)
		}
		if command.HasData() {
			pdus := splitDataIntoPDUs(sm, event.dimsePayload.abstractSyntaxName, false /*data*/, event.dimsePayload.data)
			for _, pdu := range pdus {
				sendPDU(sm, &pdu)
			}
		} else {
			doassert(len(event.dimsePayload.data) == 0)
		}
		sm.downcallCh <- stateEvent{event: evt14}
		return sta08
	}}

var actionAr8 = &stateAction{"AR-8", "Issue A-RELEASE indication (release collision): if association-requestor, next state is Sta09, if not next state is Sta10",
	func(sm *stateMachine, event stateEvent) stateType {
		if sm.isUser {
			return sta09
		}
		return sta10
	}}

var actionAr9 = &stateAction{"AR-9", "Send A-RELEASE-RP PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AReleaseRp{})
		return sta11
	}}

var actionAr10 = &stateAction{"AR-10", "Issue A-RELEASE confimation primitive",
	func(sm *stateMachine, event stateEvent) stateType {
		return sta12
	}}

// Association abort related actions
var actionAa1 = &stateAction{"AA-1", "Send A-ABORT PDU (service-user source) and start (or restart if already started) ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		diagnostic := pdu.AbortReasonType(0)
		if sm.currentState == sta02 {
			diagnostic = pdu.AbortReasonUnexpectedPDU
		}
		sendPDU(sm, &pdu.AAbort{Source: 0, Reason: diagnostic})
		sm.restartTimer()
		return sta13
	}}

var actionAa2 = &stateAction{"AA-2", "Stop ARTIM timer if running. Close transport connection",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.stopTimer()
		sm.closeConnection()
		return sta01
	}}

var actionAa3 = &stateAction{"AA-3", "If (service-user initiated abort): issue A-ABORT indication and close transport connection, otherwise (service-dul initiated abort): issue A-P-ABORT indication and close transport connection",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.closeConnection()
		return sta01
	}}

var actionAa4 = &stateAction{"AA-4", "Issue A-P-ABORT indication primitive",
	func(sm *stateMachine, event stateEvent) stateType {
		return sta01
	}}

var actionAa5 = &stateAction{"AA-5", "Stop ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		sm.stopTimer()
		return sta01
	}}

var actionAa6 = &stateAction{"AA-6", "Ignore PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		return sta13
	}}

var actionAa7 = &stateAction{"AA-7", "Send A-ABORT PDU",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AAbort{Source: 0, Reason: 0})
		return sta13
	}}

var actionAa8 = &stateAction{"AA-8", "Send A-ABORT PDU (service-dul source), issue an A-P-ABORT indication and start ARTIM timer",
	func(sm *stateMachine, event stateEvent) stateType {
		sendPDU(sm, &pdu.AAbort{Source: 2, Reason: 0})
		sm.startTimer()
		return sta13
	}}

type upcallEventType int

const (
	upcallEventHandshakeCompleted = upcallEventType(100)
	upcallEventData               = upcallEventType(101)
	// Note: connection shutdown and any error will result in channel
	// closure, so they don't have event types.
)

func (e *upcallEventType) String() string {
	var description string
	switch *e {
	case upcallEventHandshakeCompleted:
		description = "Handshake completed"
	case upcallEventData:
		description = "P_DATA_TF PDU received"
	default:
		panic(fmt.Sprintf("dicom.StateMachine: Unknown event type %v", int(*e)))
	}
	return fmt.Sprintf("upcall%02d(%s)", *e, description)
}

type upcallEvent struct {
	eventType upcallEventType

	// The context ID -> <abstract syntax uid, transefr syntax uid> mappings.
	// Sent for upcallEventHandshakeCompleted and upcallEventData.
	cm *contextManager

	// abstractSyntaxUID is extracted from the P_DATA_TF packet.
	// transferSyntaxUID is the value agreed on for the abstractSyntaxUID
	// during protocol handshake. Both are nonempty iff
	// eventType==upcallEventData.
	//abstractSyntaxUID string
	//transferSyntaxUID string

	// The context of the request. It can be mapped backto <abstract syntax, transfer syntax> by consulting the
	// context manager. Set only in upcallEventData event.
	contextID byte

	command dimse.Message
	data    []byte
}

type stateEventDIMSEPayload struct {
	// The syntax UID of the data to be sent.
	abstractSyntaxName string

	// Command to send. len(command) may exceed the max PDU size, in which case it
	// will be split into multiple PresentationDataValueItems.
	command dimse.Message

	// Ditto, but for the data payload. The data PDU is sent iff.
	// command.HasData()==true.
	data []byte
}

type stateEventDebugInfo struct {
	state stateType // the state the system was in when timer was created.
}

type stateEvent struct {
	event eventType
	pdu   pdu.PDU
	err   error
	conn  net.Conn

	dimsePayload *stateEventDIMSEPayload // set iff event==evt09.
	debug        *stateEventDebugInfo
}

func (e *stateEvent) String() string {
	debug := ""
	if e.debug != nil {
		debug = e.debug.state.String()
	}
	return fmt.Sprintf("type:%s err:%v debug:%v pdu:%v",
		e.event.String(), e.err, debug, e.pdu)
}

type stateTransition struct {
	current stateType
	event   eventType
	action  *stateAction
}

type stateTransitionKey struct {
	current stateType
	event   eventType
}

var stateTransitions = map[stateTransitionKey]*stateAction{
	{sta01, evt01}: actionAe1,
	{sta01, evt05}: actionAe5,
	{sta02, evt03}: actionAa1,
	{sta02, evt04}: actionAa1,
	{sta02, evt06}: actionAe6,
	{sta02, evt10}: actionAa1,
	{sta02, evt12}: actionAa1,
	{sta02, evt13}: actionAa1,
	{sta02, evt16}: actionAa2,
	{sta02, evt17}: actionAa5,
	{sta02, evt18}: actionAa2,
	{sta02, evt19}: actionAa1,
	{sta03, evt03}: actionAa8,
	{sta03, evt04}: actionAa8,
	{sta03, evt06}: actionAa8,
	{sta03, evt07}: actionAe7,
	{sta03, evt08}: actionAe8,
	{sta03, evt10}: actionAa8,
	{sta03, evt12}: actionAa8,
	{sta03, evt13}: actionAa8,
	{sta03, evt15}: actionAa1,
	{sta03, evt16}: actionAa3,
	{sta03, evt17}: actionAa4,
	{sta03, evt19}: actionAa8,
	{sta04, evt02}: actionAe2,
	{sta04, evt15}: actionAa2,
	{sta04, evt17}: actionAa4,
	{sta05, evt03}: actionAe3,
	{sta05, evt04}: actionAe4,
	{sta05, evt06}: actionAa8,
	{sta05, evt10}: actionAa8,
	{sta05, evt12}: actionAa8,
	{sta05, evt13}: actionAa8,
	{sta05, evt15}: actionAa1,
	{sta05, evt16}: actionAa3,
	{sta05, evt17}: actionAa4,
	{sta05, evt18}: actionAa8,
	{sta05, evt19}: actionAa8,
	{sta06, evt03}: actionAa8,
	{sta06, evt04}: actionAa8,
	{sta06, evt06}: actionAa8,
	{sta06, evt09}: actionDt1,
	{sta06, evt10}: actionDt2,
	{sta06, evt11}: actionAr1,
	{sta06, evt12}: actionAr2,
	{sta06, evt13}: actionAa8,
	{sta06, evt15}: actionAa1,
	{sta06, evt16}: actionAa3,
	{sta06, evt17}: actionAa4,
	{sta06, evt19}: actionAa8,
	{sta07, evt03}: actionAa8,
	{sta07, evt04}: actionAa8,
	{sta07, evt06}: actionAa8,
	{sta07, evt10}: actionAr6,
	{sta07, evt12}: actionAr8,
	{sta07, evt13}: actionAr3,
	{sta07, evt15}: actionAa1,
	{sta07, evt16}: actionAa3,
	{sta07, evt17}: actionAa4,
	{sta07, evt19}: actionAa8,
	{sta08, evt03}: actionAa8,
	{sta08, evt04}: actionAa8,
	{sta08, evt06}: actionAa8,
	{sta08, evt09}: actionAr7,
	{sta08, evt10}: actionAa8,
	{sta08, evt12}: actionAa8,
	{sta08, evt13}: actionAa8,
	{sta08, evt14}: actionAr4,
	{sta08, evt15}: actionAa1,
	{sta08, evt16}: actionAa3,
	{sta08, evt17}: actionAa4,
	{sta08, evt19}: actionAa8,
	{sta09, evt03}: actionAa8,
	{sta09, evt04}: actionAa8,
	{sta09, evt06}: actionAa8,
	{sta09, evt10}: actionAa8,
	{sta09, evt12}: actionAa8,
	{sta09, evt13}: actionAa8,
	{sta09, evt14}: actionAr9,
	{sta09, evt15}: actionAa1,
	{sta09, evt16}: actionAa3,
	{sta09, evt17}: actionAa4,
	{sta09, evt19}: actionAa8,
	{sta10, evt03}: actionAa8,
	{sta10, evt04}: actionAa8,
	{sta10, evt06}: actionAa8,
	{sta10, evt10}: actionAa8,
	{sta10, evt12}: actionAa8,
	{sta10, evt13}: actionAr10,
	{sta10, evt15}: actionAa1,
	{sta10, evt16}: actionAa3,
	{sta10, evt17}: actionAa4,
	{sta10, evt19}: actionAa8,
	{sta11, evt03}: actionAa8,
	{sta11, evt04}: actionAa8,
	{sta11, evt06}: actionAa8,
	{sta11, evt10}: actionAa8,
	{sta11, evt12}: actionAa8,
	{sta11, evt13}: actionAr3,
	{sta11, evt15}: actionAa1,
	{sta11, evt16}: actionAa3,
	{sta11, evt17}: actionAa4,
	{sta11, evt19}: actionAa8,
	{sta12, evt03}: actionAa8,
	{sta12, evt04}: actionAa8,
	{sta12, evt06}: actionAa8,
	{sta12, evt10}: actionAa8,
	{sta12, evt12}: actionAa8,
	{sta12, evt13}: actionAa8,
	{sta12, evt14}: actionAr4,
	{sta12, evt15}: actionAa1,
	{sta12, evt16}: actionAa3,
	{sta12, evt17}: actionAa4,
	{sta12, evt19}: actionAa8,
	{sta13, evt03}: actionAa6,
	{sta13, evt04}: actionAa6,
	{sta13, evt06}: actionAa7,
	{sta13, evt07}: actionAa7,
	{sta13, evt08}: actionAa7,
	{sta13, evt09}: actionAa7,
	{sta13, evt10}: actionAa6,
	{sta13, evt11}: actionAa6,
	{sta13, evt12}: actionAa6,
	{sta13, evt13}: actionAa6,
	{sta13, evt14}: actionAa6,
	{sta13, evt15}: actionAa2,
	{sta13, evt16}: actionAa2,
	{sta13, evt17}: actionAr5,
	{sta13, evt18}: actionAa2,
	{sta13, evt19}: actionAa7,
}

func findAction(currentState stateType, event *stateEvent) *stateAction {
	key := stateTransitionKey{currentState, event.event}
	if action, ok := stateTransitions[key]; ok {
		return action
	}
	return nil
}

// Per-TCP-connection state.
type stateMachine struct {
	label  string // For logging only
	isUser bool   // true if service user, false if provider

	// userParams is set only for a client-side statemachine
	userParams ServiceUserParams

	// Manages mappings between one-byte contextID to the
	// <abstractsyntaxUID, transfersyntaxuid> pair.  Filled during A_ACCEPT
	// handshake.
	contextManager *contextManager

	// For receiving PDU and network status events.
	// Owned by networkReaderThread.
	netCh chan stateEvent

	// For reporting errors to this event.  Owned by the statemachine.
	errorCh chan stateEvent

	// For receiving commands from the upper layer
	// Owned by the upper layer.
	downcallCh chan stateEvent

	// For sending indications to the the upper layer. Owned by the
	// statemachine.
	upcallCh chan upcallEvent

	// For Timer expiration event
	timerCh chan stateEvent

	// The socket to the remote peer.
	conn         net.Conn
	currentState stateType

	// For assembling DIMSE command from multiple P_DATA_TF fragments.
	commandAssembler dimse.CommandAssembler

	// Only for testing.
	faults FaultInjector
}

func (sm *stateMachine) closeConnection() {
	close(sm.upcallCh)
	dicomlog.Vprintf(1, "dicom.StateMachine %s: Closing connection %v", sm.label, sm.conn)
	if sm.conn != nil {
		sm.conn.Close()
	}
}

func sendPDU(sm *stateMachine, v pdu.PDU) {
	doassert(sm.conn != nil)
	data, err := pdu.EncodePDU(v)
	if err != nil {
		dicomlog.Vprintf(0, "dicom.StateMachine %s: Failed to encode: %v; closing connection %v", sm.label, err, sm.conn)
		sm.conn.Close()
		sm.errorCh <- stateEvent{event: evt17, err: err}
		return
	}
	if sm.faults != nil {
		action := sm.faults.onSend(data)
		if action == faultInjectorDisconnect {
			dicomlog.Vprintf(0, "dicom.StateMachine %s: FAULT: closing connection for test", sm.label)
			sm.conn.Close()
		}
	}
	n, err := sm.conn.Write(data)
	if n != len(data) || err != nil {
		dicomlog.Vprintf(0, "dicom.StateMachine %s: Failed to write %d bytes. Actual %d bytes : %v; closing connection %v", sm.label, len(data), n, err, sm.conn)
		sm.conn.Close()
		sm.errorCh <- stateEvent{event: evt17, err: err}
		return
	}
	dicomlog.Vprintf(2, "dicom.StateMachine %s: sendPDU: %v", sm.label, v.String())
}

func (sm *stateMachine) startTimer() {
	ch := make(chan stateEvent, 1)
	sm.timerCh = ch
	currentState := sm.currentState
	time.AfterFunc(time.Duration(10)*time.Second,
		func() {
			ch <- stateEvent{event: evt18, debug: &stateEventDebugInfo{currentState}}
			close(ch)
		})
}

func (sm *stateMachine) restartTimer() {
	sm.startTimer()
}

func (sm *stateMachine) stopTimer() {
	sm.timerCh = make(chan stateEvent, 1)
}

func networkReaderThread(ch chan stateEvent, conn net.Conn, maxPDUSize int, smName string) {
	dicomlog.Vprintf(2, "dicom.StateMachine %s: Starting network reader, maxPDU %d", smName, maxPDUSize)
	doassert(maxPDUSize > 16*1024)
	for {
		v, err := pdu.ReadPDU(conn, maxPDUSize)
		if err != nil {
			dicomlog.Vprintf(0, "dicom.StateMachine %s: Failed to read PDU: %v,", smName, err)
			if err == io.EOF {
				ch <- stateEvent{event: evt17, pdu: nil, err: nil}
			} else {
				ch <- stateEvent{event: evt19, pdu: nil, err: err}
			}
			close(ch)
			break
		}
		dicomlog.Vprintf(0, "dicom.StateMachine %s: read PDU: %v", smName, v.String())
		doassert(v != nil)
		dicomlog.Vprintf(2, "dicom.StateMachine %s: read PDU: %v", smName, v.String())
		switch n := v.(type) {
		case *pdu.AAssociateRQ:
			ch <- stateEvent{event: evt06, pdu: n, err: nil}
			continue
		case *pdu.AAssociateAC:
			ch <- stateEvent{event: evt03, pdu: n, err: nil}
			continue
		case *pdu.AAssociateRj:
			dicomlog.Vprintf(0, "dicom.StateMachine %s: Association rejected: %v", smName, v.String())
			ch <- stateEvent{event: evt04, pdu: n, err: nil}
			continue
		case *pdu.PDataTf:
			ch <- stateEvent{event: evt10, pdu: n, err: nil}
			continue
		case *pdu.AReleaseRq:
			ch <- stateEvent{event: evt12, pdu: n, err: nil}
			continue
		case *pdu.AReleaseRp:
			ch <- stateEvent{event: evt13, pdu: n, err: nil}
			continue
		case *pdu.AAbort:
			dicomlog.Vprintf(0, "dicom.StateMachine %s: Association aborted: %v", smName, v.String())
			ch <- stateEvent{event: evt16, pdu: n, err: nil}
			continue
		default:
			err := fmt.Errorf("dicom.StateMachine %s: Unknown PDU type: %v", v.String(), smName)
			ch <- stateEvent{event: evt19, pdu: v, err: err}
			dicomlog.Vprintf(0, "dicom.StateMachine: %v", err)
			continue
		}
	}
	dicomlog.Vprintf(2, "dicom.StateMachine %s: Exiting network reader", smName)
}

func (sm *stateMachine) getNextEvent() stateEvent {
	var ok bool
	var event stateEvent
	for event.event == 0 {
		select {
		case event, ok = <-sm.netCh:
			if !ok {
				sm.netCh = nil
			}
		case event = <-sm.errorCh:
			// this channel shall never close.
		case event, ok = <-sm.timerCh:
			if !ok {
				sm.timerCh = nil
			}
		case event, ok = <-sm.downcallCh:
			if !ok {
				sm.downcallCh = nil
			}
		}
	}
	switch event.event {
	case evt02:
		doassert(event.conn != nil)
		sm.conn = event.conn
	case evt17:
		close(sm.upcallCh)
		sm.conn = nil
	}
	return event
}

func (sm *stateMachine) runOneStep() {
	event := sm.getNextEvent()
	dicomlog.Vprintf(2, "dicom.StateMachine %s: Current state: %v, Event %v", sm.label, sm.currentState.String(), event)
	action := findAction(sm.currentState, &event)
	if action == nil {
		msg := fmt.Sprintf("dicom.StateMachine %s: No action found for state %v, event %v", sm.label, sm.currentState.String(), event.String())
		if sm.faults != nil {
			msg += " FIhistory: " + sm.faults.String()
		}
		dicomlog.Vprintf(0, "dicom.StateMachine: Unknown state transition:")
		for _, s := range strings.Split(msg, "\n") {
			dicomlog.Vprintf(0, s)
		}
		dicomlog.Vprintf(0, msg)

		action = actionAa2 // This will force connection abortion
	}
	dicomlog.Vprintf(2, "dicom.StateMachine %s: Running action %v", sm.label, action)
	newState := action.Callback(sm, event)
	if sm.faults != nil {
		sm.faults.onStateTransition(sm.currentState, &event, action, newState)
	}
	sm.currentState = newState
	dicomlog.Vprintf(2, "dicom.StateMachine Next state: %v", sm.currentState.String())
}

func runStateMachineForServiceUser(
	params ServiceUserParams,
	upcallCh chan upcallEvent,
	downcallCh chan stateEvent,
	label string) {
	doassert(params.CallingAETitle != "")
	doassert(len(params.SOPClasses) > 0)
	doassert(len(params.TransferSyntaxes) > 0)
	sm := &stateMachine{
		label:          label,
		isUser:         true,
		contextManager: newContextManager(label),
		userParams:     params,
		netCh:          make(chan stateEvent, 128),
		errorCh:        make(chan stateEvent, 128),
		downcallCh:     downcallCh,
		upcallCh:       upcallCh,
		faults:         getUserFaultInjector(),
	}
	event := stateEvent{event: evt01}
	action := findAction(sta01, &event)
	sm.currentState = action.Callback(sm, event)
	for sm.currentState != sta01 {
		sm.runOneStep()
	}
	dicomlog.Vprintf(1, "dicom.StateMachine(%s): statemachine finished", sm.label)
}

func runStateMachineForServiceProvider(
	conn net.Conn,
	upcallCh chan upcallEvent,
	downcallCh chan stateEvent,
	label string) {
	sm := &stateMachine{
		label:          label,
		isUser:         false,
		contextManager: newContextManager(label),
		conn:           conn,
		netCh:          make(chan stateEvent, 128),
		errorCh:        make(chan stateEvent, 128),
		downcallCh:     downcallCh,
		upcallCh:       upcallCh,
		faults:         getProviderFaultInjector(),
	}
	event := stateEvent{event: evt05, conn: conn}
	action := findAction(sta01, &event)
	sm.currentState = action.Callback(sm, event)
	for sm.currentState != sta01 {
		sm.runOneStep()
	}
	dicomlog.Vprintf(1, "dicom.StateMachine %s: statemachine finished", sm.label)
}
