package tftp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

func NewServer(readHandler func(filename string, rf io.ReaderFrom) error,
	writeHandler func(filename string, wt io.WriterTo) error) *Server {
	return &Server{
		readHandler:  readHandler,
		writeHandler: writeHandler,
		timeout:      defaultTimeout,
	}
}

type Server struct {
	readHandler  func(filename string, rf io.ReaderFrom) error
	writeHandler func(filename string, wt io.WriterTo) error
	conn         *net.UDPConn
	quit         chan chan struct{}
	wg           sync.WaitGroup
	timeout      time.Duration
}

func (s *Server) SetTimeout(t time.Duration) {
	s.timeout = t
}

func (s *Server) ListenAndServe(addr string) error {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", a)
	if err != nil {
		return err
	}
	s.Serve(conn)
	return nil
}

func (s *Server) Serve(conn *net.UDPConn) {
	s.conn = conn
	s.quit = make(chan chan struct{})
	for {
		select {
		case q := <-s.quit:
			q <- struct{}{}
			return
		default:
			err := s.processRequest(s.conn)
			if err != nil {
				// TODO: add logging handler
			}
		}
	}
}

func (s *Server) Shutdown() {
	s.conn.Close()
	q := make(chan struct{})
	s.quit <- q
	<-q
	s.wg.Wait()
}

func (s *Server) processRequest(conn *net.UDPConn) error {
	var buffer []byte
	buffer = make([]byte, datagramLength)
	n, remoteAddr, err := conn.ReadFromUDP(buffer)
	if err != nil {
		return fmt.Errorf("reading UDP: %v", err)
	}
	p, err := parsePacket(buffer[:n])
	if err != nil {
		return err
	}
	switch p := p.(type) {
	case pWRQ:
		filename, mode, err := unpackRQ(p)
		if err != nil {
			return fmt.Errorf("unpack WRQ: %v", err)
		}
		//fmt.Printf("got WRQ (filename=%s, mode=%s)\n", filename, mode)
		transmissionConn, err := transmissionConn()
		if err != nil {
			return fmt.Errorf("open transmission: %v", err)
		}
		wt := &receiver{
			send:    make([]byte, datagramLength),
			receive: make([]byte, datagramLength),
			conn:    transmissionConn,
			retry:   &backoff{},
			timeout: s.timeout,
			addr:    remoteAddr,
			mode:    mode,
		}
		binary.BigEndian.PutUint16(wt.send[0:2], opACK)
		s.wg.Add(1)
		go func() {
			err := s.writeHandler(filename, wt)
			if err != nil {
				wt.abort(err)
			} else {
				wt.terminate()
			}
			s.wg.Done()
		}()
	case pRRQ:
		filename, mode, err := unpackRQ(p)
		if err != nil {
			return fmt.Errorf("unpack RRQ: %v", err)
		}
		//fmt.Printf("got RRQ (filename=%s, mode=%s)\n", filename, mode)
		transmissionConn, err := transmissionConn()
		if err != nil {
			return fmt.Errorf("open transmission: %v", err)
		}
		rf := &sender{
			send:    make([]byte, datagramLength),
			receive: make([]byte, datagramLength),
			conn:    transmissionConn,
			retry:   &backoff{},
			timeout: s.timeout,
			addr:    remoteAddr,
			block:   1,
			mode:    mode,
		}
		binary.BigEndian.PutUint16(rf.send[0:2], opDATA)
		s.wg.Add(1)
		go func() {
			err := s.readHandler(filename, rf)
			if err != nil {
				rf.abort(err)
			}
			s.wg.Done()
		}()
	default:
		return fmt.Errorf("unexpected %T", p)
	}
	return nil
}
