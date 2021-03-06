package fhe

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/gob"
	"fmt"
	"github.com/ldsec/lattigo/bfv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	serverUrl string
	token     string
}

func NewClient(serverUrl, token string) *Client {
	return &Client{
		serverUrl: serverUrl,
		token:     token,
	}
}

// EvalReq sends evaluation request to server, decrypts result and returns it
func (cl *Client) EvalReq(params *bfv.Parameters, fromTimestamp, toTimestamp int64) ([]uint64, error) {
	conn, err := grpc.Dial(cl.serverUrl, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := NewFhesrvClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*20)
	ctx = metadata.AppendToOutgoingContext(ctx, "token", cl.token)
	defer cancel()
	req, err := cl.prepareRequest(params)
	if err != nil {
		return nil, err
	}
	r, err := c.EvalFiles(ctx, &EvalRequest{
		Request:       req,
		Fromtimestamp: fromTimestamp,
		Totimestamp:   toTimestamp,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("files: %s", r.GetMessage())
	rb := bytes.NewBuffer(r.GetResponse())
	decoder := gob.NewDecoder(rb)
	inr := In{}
	if err = decoder.Decode(&inr.Res); err != nil {
		return nil, err
	}
	return decResult(params, &inr.Res), nil
}

// WriteFromFile writes to server multiple vectors stored in a csv file with each vector being one row in a file.
func (cl *Client) WriteFromFile(params *bfv.Parameters, filename string) error {
	var data [][]uint64
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	r := csv.NewReader(file)
	rows, err := r.ReadAll()
	if err != nil {
		return err
	}
	for _, row := range rows {
		var dataRow []uint64
		for _, val := range row {
			if strings.Trim(val, " ") == "" {
				dataRow = append(dataRow, 0)
				continue
			}
			u, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return err
			}
			dataRow = append(dataRow, u)
		}
		data = append(data, dataRow)
	}
	return cl.WriteMulti(params, data)
}

// WriteMulti writes multiple vectors to server.
func (cl *Client) WriteMulti(params *bfv.Parameters, data [][]uint64) error {
	for _, dataRow := range data {
		err := cl.Write(params, dataRow...)
		if err != nil {
			return err
		}
	}
	return nil
}

// Write writes supplied vector to server.
func (cl *Client) Write(params *bfv.Parameters, values ...uint64) error {
	conn, err := grpc.Dial(cl.serverUrl, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := NewFhesrvClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*20)
	ctx = metadata.AppendToOutgoingContext(ctx, "token", cl.token)
	defer cancel()
	_, publicKey := readKeys()
	enc := bfv.NewEncryptorFromPk(params, publicKey)
	encoder := bfv.NewEncoder(params)

	filePlaintext := bfv.NewPlaintext(params)
	encoder.EncodeUint(values, filePlaintext)
	FilesCiphertext := enc.EncryptNew(filePlaintext)
	b := bytes.Buffer{}
	gobEncoder := gob.NewEncoder(&b)
	err = gobEncoder.Encode(FilesCiphertext)
	if err != nil {
		return err
	}
	_, err = c.UploadFile(ctx, &UploadRequest{
		File: b.Bytes(),
	})
	return err
}

func (cl *Client) GenKeys(params *bfv.Parameters) {
	kgen := bfv.NewKeyGenerator(params)
	riderSk, riderPk := kgen.GenKeyPair()
	filepk, err := os.Create("enc.pk")
	if err != nil {
		os.Exit(1)
	}
	encoder := gob.NewEncoder(filepk)
	encoder.Encode(riderPk)
	filepk.Close()
	filesk, err := os.Create("enc.sk")
	if err != nil {
		os.Exit(1)
	}
	encodersk := gob.NewEncoder(filesk)
	encodersk.Encode(riderSk)
	filesk.Close()
}

func (cl *Client) prepareUpload(params *bfv.Parameters) []byte {
	resPlaintext := bfv.NewPlaintext(params)
	secretKey, _ := readKeys()
	encryptorSk := bfv.NewEncryptorFromSk(params, secretKey)
	res := encryptorSk.EncryptNew(resPlaintext)
	b := bytes.Buffer{}
	encoder := gob.NewEncoder(&b)
	out := In{
		Params: *params,
		Res:    *res,
	}
	err := encoder.Encode(&out)
	if err != nil {
		fmt.Println("Fatal error ", err.Error())
		os.Exit(1)
	}
	return b.Bytes()
}

func (cl *Client) prepareRequest(params *bfv.Parameters) ([]byte, error) {
	resPlaintext := bfv.NewPlaintext(params)
	secretKey, _ := readKeys()
	encryptorSk := bfv.NewEncryptorFromSk(params, secretKey)
	res := encryptorSk.EncryptNew(resPlaintext)
	b := bytes.Buffer{}
	encoder := gob.NewEncoder(&b)
	out := In{
		Params: *params,
		Res:    *res,
	}
	err := encoder.Encode(&out)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func readKeys() (*bfv.SecretKey, *bfv.PublicKey) {
	file, err := os.Open("enc.sk")
	if err != nil {
		os.Exit(1)
	}
	decoder := gob.NewDecoder(file)
	riderSk := bfv.SecretKey{}
	decoder.Decode(&riderSk)
	filepk, err := os.Open("enc.pk")
	if err != nil {
		os.Exit(1)
	}
	decoderpk := gob.NewDecoder(filepk)
	riderPk := bfv.PublicKey{}
	decoderpk.Decode(&riderPk)
	return &riderSk, &riderPk
}

func decResult(params *bfv.Parameters, res *bfv.Ciphertext) []uint64 {
	encoder := bfv.NewEncoder(params)
	riderSk, _ := readKeys()
	decryptor := bfv.NewDecryptor(params, riderSk)
	return encoder.DecodeUint(decryptor.DecryptNew(res))
}
