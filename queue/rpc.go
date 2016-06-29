package queue

import "errors"

type RPCReq struct {
	method    string
	replyChan chan interface{}
}

type RPCResponse string

type Status struct {
	queueChan chan RPCReq
}

type RPCArgs struct{}

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
		break
	}

	return nil
}
