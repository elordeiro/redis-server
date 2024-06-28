package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Handler entry point --------------------------------------------------------
func (s *Server) Handler(parsedResp *RESP, conn *ConnRW) (resp []*RESP) {
	switch parsedResp.Type {
	case ERROR, INTEGER, BULK, STRING:
		return []*RESP{{Type: ERROR, Value: "Response type " + parsedResp.Value + " handle not yet implemented"}}
	case ARRAY:
		return s.handleArray(parsedResp, conn)
	case RDB:
		return []*RESP{s.decodeRDB(NewBuffer(bytes.NewReader([]byte(parsedResp.Value))))}
	default:
		return []*RESP{{Type: ERROR, Value: "Response type " + parsedResp.Value + " not recognized"}}
	}
}

func (s *Server) handleArray(resp *RESP, conn *ConnRW) []*RESP {
	command, args := resp.getCmdAndArgs()
	switch command {
	case "PING":
		return []*RESP{ping(args)}
	case "ECHO":
		return []*RESP{echo(args)}
	case "SET":
		s.propagateCommand(resp)
		return []*RESP{s.set(args)}
	case "GET":
		return []*RESP{s.get(args)}
	case "INFO":
		return []*RESP{info(args, s.Role.String(), s.MasterReplid, s.MasterReplOffset)}
	case "REPLCONF":
		s.replConfig(args, conn)
		return []*RESP{}
	case "PSYNC":
		conn.Type = REPLICA
		s.ReplicaCount++
		defer func() {
			// go s.checkOnReplica(w)
		}()
		return []*RESP{psync(s.MasterReplid, s.MasterReplOffset), getRDB()}
	case "WAIT":
		return []*RESP{s.wait(args)}
	case "KEYS":
		return []*RESP{s.keys(args)}
	case "COMMAND":
		return []*RESP{commandFunc()}
	case "CONFIG":
		return []*RESP{s.config(args)}
	default:
		return []*RESP{{Type: ERROR, Value: "Unknown command " + command}}
	}
}

func (s *Server) propagateCommand(resp *RESP) {
	for _, conn := range s.Conns {
		if conn.Type != REPLICA {
			continue
		}
		marshaled := resp.Marshal()
		s.MasterReplOffset += len(marshaled)
		Write(conn.Writer, marshaled)
	}
}

func (s *Server) checkOnReplica(w *Writer) {
	getAckResp := GetAckResp().Marshal()
	n := len(getAckResp)
	for {
		time.Sleep(5 * time.Second)
		fmt.Println("Checking On Replica")
		s.MasterReplOffset += n
		Write(w, getAckResp)
	}
}

// ----------------------------------------------------------------------------

// Predefined responses -------------------------------------------------------
func OkResp() *RESP {
	return &RESP{Type: STRING, Value: "OK"}
}

func NullResp() *RESP {
	return &RESP{Type: NULL}
}

func ErrResp(err string) *RESP {
	return &RESP{Type: ERROR, Value: err}
}

func GetAckResp() *RESP {
	return &RESP{
		Type: ARRAY,
		Values: []*RESP{
			{Type: BULK, Value: "REPLCONF"},
			{Type: BULK, Value: "GETACK"},
			{Type: BULK, Value: "*"},
		},
	}
}

// ----------------------------------------------------------------------------

// Handshake helpers ----------------------------------------------------------
// Can be used for handshake stage 1
func PingResp() *RESP {
	return &RESP{
		Type: ARRAY,
		Values: []*RESP{
			{
				Type: BULK, Value: "PING",
			},
		},
	}
}

// Can be used for handshake stage 2
func ReplconfResp(i int, port string) *RESP {
	switch i {
	case 1:
		return &RESP{
			Type: ARRAY,
			Values: []*RESP{
				{Type: BULK, Value: "REPLCONF"},
				{Type: BULK, Value: "listening-port"},
				{Type: BULK, Value: port},
			},
		}
	case 2:
		return &RESP{
			Type: ARRAY,
			Values: []*RESP{
				{Type: BULK, Value: "REPLCONF"},
				{Type: BULK, Value: "capa"},
				{Type: BULK, Value: "psync2"},
			},
		}
	default:
		return NullResp()
	}

}

// Can be used for handshake stage 3 as Replica
func Psync(replId, offset int) *RESP {
	replIdStr, offsetStr := "", ""
	switch replId {
	case 0:
		replIdStr, offsetStr = "?", "-1"
	default:
		replIdStr = strconv.Itoa(replId)
		offsetStr = strconv.Itoa(offset)
	}

	return &RESP{
		Type: ARRAY,
		Values: []*RESP{
			{Type: BULK, Value: "PSYNC"},
			{Type: BULK, Value: replIdStr},
			{Type: BULK, Value: offsetStr},
		},
	}
}

// Can be used for handshake stage 3 as Master
const EmptyRBD = "524544495330303131fa0972656469732d76657205372e322e30fa0a72656469732d62697473c040fa056374696d65c26d08bc65fa08757365642d6d656dc2b0c41000fa08616f662d62617365c000fff06e3bfec0ff5aa2"

func getRDB() *RESP {
	return &RESP{
		Type:  RDB,
		Value: EmptyRBD,
	}
}

func psync(mrid string, mros int) *RESP {
	return &RESP{
		Type:  STRING,
		Value: "FULLRESYNC " + mrid + " " + strconv.Itoa(mros),
	}
}

// ----------------------------------------------------------------------------

// Assert Responses -----------------------------------------------------------
func (resp *RESP) IsOkay() bool {
	if resp.Type != STRING {
		return false
	}
	if resp.Value != "OK" {
		return false
	}
	return true
}

func (resp *RESP) IsPong() bool {
	if resp.Type != STRING {
		return false
	}
	if resp.Value != "PONG" {
		return false
	}
	return true
}

// ----------------------------------------------------------------------------

// General commands -----------------------------------------------------------
func commandFunc() *RESP {
	return &RESP{Type: NULL, Value: "Command"}
}

func ping(args []*RESP) *RESP {
	if len(args) == 0 {
		return &RESP{Type: STRING, Value: "PONG"}
	}
	return &RESP{Type: STRING, Value: args[0].Value}
}

func echo(args []*RESP) *RESP {
	if len(args) == 0 {
		return &RESP{Type: STRING, Value: ""}
	}
	return &RESP{Type: STRING, Value: args[0].Value}
}

func info(args []*RESP, role, mrid string, mros int) *RESP {
	if len(args) != 1 {
		return NullResp()
	}
	switch args[0].Value {
	case "replication":
		return &RESP{
			Type: BULK,
			Value: "# Replication\n" +
				"role:" + role + "\n" +
				"master_replid:" + mrid + "\n" +
				"master_repl_offset:" + strconv.Itoa(mros) + "\n",
		}
	default:
		return NullResp()
	}
}

// ----------------------------------------------------------------------------

// Server specific commands ---------------------------------------------------
func decodeSize(r *bufio.Reader) (int, error) {
	bt, _ := r.ReadByte()
	switch bt >> 6 {
	case 0:
		return int(bt), nil
	case 1:
		next, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		return int(bt&0x3F)<<8 | int(next), nil
	case 2:
		next4 := make([]byte, 4)
		_, err := io.ReadFull(r, next4)
		if err != nil {
			return 0, err
		}
		return int(next4[0])<<24 | int(next4[1])<<16 | int(next4[2])<<8 | int(next4[3]), nil
	default:
		return 0, errors.New("error decoding size bytes")
	}
}

func decodeString(r *bufio.Reader) (string, error) {
	bt, _ := r.ReadByte()
	switch {
	case bt < 0xc0:
		str := make([]byte, int(bt))
		io.ReadFull(r, str)
		return string(str), nil
	case bt == 0xC0:
		next, _ := r.ReadByte()
		return strconv.Itoa(int(next)), nil
	case bt == 0xC1:
		next2 := make([]byte, 2)
		_, err := io.ReadFull(r, next2)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(next2[1])<<8 | int(next2[0])), nil
	case bt == 0xC2:
		next4 := make([]byte, 4)
		_, err := io.ReadFull(r, next4)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(int(next4[3])<<24 | int(next4[2])<<16 | int(next4[1])<<8 | int(next4[0])), nil
	case bt == 0xC3:
		return "", errors.New("LZF compression not supported")
	default:
		return "", errors.New("error decoding string")
	}
}

func dedodeTime(r *bufio.Reader) (int64, error) {
	byt, _ := r.ReadByte()
	var expiryTime int64 = 0
	if byt == 0xfc {
		expiry := make([]byte, 8)
		_, err := io.ReadFull(r, expiry)
		if err != nil {
			return 0, err
		}
		expiryTime =
			int64(expiry[7])<<56 | int64(expiry[6])<<48 | int64(expiry[5])<<40 | int64(expiry[4])<<32 |
				int64(expiry[3])<<24 | int64(expiry[2])<<16 | int64(expiry[1])<<8 | int64(expiry[0])
	} else if byt == 0xfd {
		expiry := make([]byte, 4)
		_, err := io.ReadFull(r, expiry)
		if err != nil {
			return 0, err
		}
		expiryTime = int64(expiry[3])<<24 | int64(expiry[2])<<16 | int64(expiry[1])<<8 | int64(expiry[0])
	} else {
		r.UnreadByte()
		return 0, nil
	}
	return expiryTime, nil
}

func (s *Server) decodeRDB(buf *Buffer) *RESP {
	data := buf.reader

	// Header section
	header := make([]byte, 9)
	_, err := io.ReadFull(data, header)
	if err != nil {
		return ErrResp("Error reading RDB header")
	}

	if string(header[:5]) != "REDIS" {
		return ErrResp("Invalid RDB file")
	}

	// version := string(header[5:])
	// if version != "0007" {
	// 	return ErrResp("Invalid RDB version")
	// }

	// Metadata section
	for {
		fa, err := data.ReadByte()
		if err != nil {
			return ErrResp("Error reading metadata section")
		}
		if fa != 0xfa {
			data.UnreadByte()
			break
		}

		// Metadataa Key
		_, err = decodeString(data)
		if err != nil {
			return ErrResp("Error reading metadata section")
		}
		// Metadata Value
		_, err = decodeString(data)
		if err != nil {
			return ErrResp("Error reading metadata section")
		}
	}

	for {
		byt, _ := data.Peek(1)
		if byt[0] == 0xff {
			break
		}
		// Database section - 0xfe
		data.ReadByte()

		// This byte is the database index
		// TODO - Implement support for multiple databases
		decodeSize(data)

		fb, err := data.ReadByte()
		if err != nil || fb != 0xfb {
			return ErrResp("Error reading database section")
		}

		dbsize, err := decodeSize(data)
		if err != nil {
			return ErrResp("Error reading database section")
		}

		// Expiry size
		_, err = decodeSize(data)
		if err != nil {
			return ErrResp("Error reading database section")
		}

		// Iterate over keys
		for i := 0; i < dbsize; i++ {
			// Expiry
			expiryTime, err := dedodeTime(data)
			if err != nil {
				return ErrResp("Error reading expiry")
			}

			// This byte is the key type
			// TODO - Implement support for different key types
			data.ReadByte()

			// Key
			key, err := decodeString(data)
			if err != nil {
				return ErrResp("Error reading key")
			}

			// Value
			value, err := decodeString(data)
			if err != nil {
				return ErrResp("Error reading value")
			}

			s.SETsMu.Lock()
			s.SETs[string(key)] = string(value)
			if expiryTime > 0 {
				s.EXP[string(key)] = expiryTime
				fmt.Println("Key: ", key, "Value: ", value, "Expiry: ", expiryTime)
			}
			s.SETsMu.Unlock()
		}

		next, _ := data.Peek(1)
		if next[0] == 0xff {
			break
		}
	}
	return OkResp()
}

func (s *Server) keys(args []*RESP) *RESP {
	if len(args) != 1 {
		return &RESP{Type: ERROR, Value: "ERR wrong number of arguments for 'keys' command"}
	}

	pattern := args[0].Value
	keys := []string{}

	if pattern == "*" {
		s.SETsMu.Lock()
		for k := range s.SETs {
			keys = append(keys, k)
		}
		s.SETsMu.Unlock()
	} else {
		s.SETsMu.Lock()
		for k := range s.SETs {
			if strings.Contains(k, pattern) {
				keys = append(keys, k)
			}
		}
		s.SETsMu.Unlock()
	}

	return &RESP{
		Type:   ARRAY,
		Values: ToRespArray(keys),
	}
}

func (s *Server) set(args []*RESP) *RESP {
	if !(len(args) == 2 || len(args) == 4) {
		return &RESP{Type: ERROR, Value: "ERR wrong number of arguments for 'set' command"}
	}
	s.NeedAcks = true
	var length int
	if len(args) > 2 {
		if strings.ToLower(args[2].Value) != "px" {
			return &RESP{Type: ERROR, Value: "ERR syntax error"}
		}

		l, err := strconv.Atoi(args[3].Value)
		if err != nil {
			return &RESP{Type: ERROR, Value: "ERR value is not an integer or out of range"}
		}
		length = l
	}

	key, value := args[0].Value, args[1].Value

	s.SETsMu.Lock()
	s.SETs[key] = value
	if length > 0 {
		// Set expiry time in milliseconds
		s.EXP[key] = time.Now().Add(time.Duration(length) * time.Millisecond).UnixMilli()
	}
	s.SETsMu.Unlock()

	return OkResp()
}

func (s *Server) get(args []*RESP) *RESP {
	if len(args) != 1 {
		return &RESP{Type: ERROR, Value: "ERR wrong number of arguments for 'get' command"}
	}

	key := args[0].Value

	s.SETsMu.Lock()
	value, ok := s.SETs[key]
	if exp, ok := s.EXP[key]; ok {
		expTime := time.UnixMilli(exp)
		if time.Now().After(expTime) {
			delete(s.SETs, key)
			delete(s.EXP, key)
			s.SETsMu.Unlock()
			return NullResp()
		}
	}
	s.SETsMu.Unlock()

	if !ok {
		return NullResp()
	}

	return &RESP{Type: STRING, Value: value}
}

func (s *Server) replConfig(args []*RESP, conn *ConnRW) (resp *RESP) {
	if len(args) != 2 {
		return &RESP{Type: ERROR, Value: "ERR wrong number of arguments for 'replconf' command"}
	}

	if strings.ToUpper(args[0].Value) == "GETACK" && args[1].Value == "*" {
		// Replica recieved REPLCONF GETACK * -> Send ACK <offset> to master
		resp = &RESP{
			Type: ARRAY,
			Values: []*RESP{
				{Type: BULK, Value: "REPLCONF"},
				{Type: BULK, Value: "ACK"},
				{Type: BULK, Value: strconv.Itoa(s.MasterReplOffset)},
			},
		}
		fmt.Println("Response: ", resp)
		Write(conn.Writer, resp)
	} else if strings.ToUpper(args[0].Value) == "ACK" {
		// Master recieved REPLCONF ACK <offset> from replica -> Read <offset> from replica
		resp = &RESP{
			Type:  INTEGER,
			Value: args[1].Value,
		}
	} else {
		// Master recieved REPLCONF listening-port <port> or REPLCONF capa psync2 from replica -> Do nothing
		resp = OkResp()
		Write(conn.Writer, resp)
	}
	return resp
}

func (s *Server) wait(args []*RESP) *RESP {
	if !s.NeedAcks {
		return &RESP{Type: INTEGER, Value: strconv.Itoa(s.ReplicaCount)}
	}
	getAck := GetAckResp().Marshal()
	defer func() {
		s.MasterReplOffset += len(getAck)
		s.Redirect = false
		s.NeedAcks = false
		fmt.Println("")
	}()

	numReplicas, _ := strconv.Atoi(args[0].Value)
	timeout, _ := strconv.Atoi(args[1].Value)

	timeoutChan := time.After(time.Duration(timeout) * time.Millisecond)
	acks := 0

	s.Redirect = true
	go func() {
		for _, c := range s.Conns {
			if c.Type != REPLICA {
				continue
			}
			Write(c.Writer, getAck)
		}
	}()

	for {
		select {
		case <-timeoutChan:
			return &RESP{
				Type:  INTEGER,
				Value: strconv.Itoa(acks),
			}
		default:
			for _, c := range s.Conns {
				if c.Type != REPLICA {
					continue
				}
				select {
				case parsedResp := <-c.Chan:
					fmt.Println("Received ACK from replica")
					_, args := parsedResp.getCmdAndArgs()
					result := s.replConfig(args, c)
					strconv.Atoi(result.Value)
					// replOffset, _ := strconv.Atoi(result.Value)
					// if replOffset == s.MasterReplOffset {
					acks++
					if acks == numReplicas {
						return &RESP{
							Type:  INTEGER,
							Value: strconv.Itoa(acks),
						}
					}
					// }
				case <-timeoutChan:
					return &RESP{
						Type:  INTEGER,
						Value: strconv.Itoa(acks),
					}
				default:
					continue
				}
			}
		}
	}
}

func (s *Server) config(args []*RESP) *RESP {
	if strings.ToUpper(args[0].Value) == "GET" {
		if strings.ToLower(args[1].Value) == "dir" {
			return &RESP{
				Type: ARRAY,
				Values: []*RESP{
					{Type: STRING, Value: "dir"},
					{Type: STRING, Value: s.Dir},
				},
			}
		}
		return &RESP{
			Type: ARRAY,
			Values: []*RESP{
				{Type: STRING, Value: "dbfilename"},
				{Type: STRING, Value: s.Dbfilename},
			},
		}
	}
	return &RESP{
		Type:  ERROR,
		Value: "ERR unknown subcommand or wrong number of arguments",
	}
}

// ----------------------------------------------------------------------------
