package queue

import "errors"

// RPCReq contains information for communicating with the RPC server
type RPCReq struct {
	method    string
	replyChan chan interface{}
}

// RPCResponse holds the response
type RPCResponse string

// Status contains RPC request information
type Status struct {
	queueChan chan RPCReq
}

// RPCArgs arg information for the RPC server
type RPCArgs struct{}

// Downloads gets download statuses
func (s *Status) Downloads(_ RPCArgs, reply *RPCResponse) error {
	replyChan := make(chan interface{})
	req := RPCReq{
		method:    "download_status",
		replyChan: replyChan,
	}

	s.queueChan <- req
	qReply := <-replyChan

	switch qReply.(type) {
	case string:
		*reply = RPCResponse(qReply.(string))
		break
	default:
		return errors.New("error: unexpected return value")
	}

	return nil
}
