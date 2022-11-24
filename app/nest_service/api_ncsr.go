/*
 * Nebula Enrollment over Secure Transport - OpenAPI 3.0
 *
 * This is a simple Public Key Infrastructure Management Server based on the RFC7030 Enrollment over Secure Transport Protocol for a Nebula Mesh Network. The Service accepts requests from mutually authenticated TLS-PSK connections to create Nebula Certificates for the client, either by signing client-generated Nebula Public Keys or by generating Nebula key pairs and signing the server-generated Nebula public key and to create Nebula configuration files for the specific client. This Service acts as a Facade for the Nebula CA service (actually signign or creating the Nebula keys) and the Nebula Config service (actually creating the nebula Config. files).
 *
 * API version: 0.1.1
 * Contact: gianmarco.decola@studio.unibo.it
 * Generated by: Swagger Codegen (https://github.com/swagger-api/swagger-codegen.git)
 */
//TODO: refactor logging and fix concurrent writing on file
package nest_service

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/m4rkdc/nebula_est/pkg/models"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func isValidHostname(str string, filepath string) bool {

	b, err := os.ReadFile(filepath)
	if err != nil {
		panic(err)
	}

	isValid, err := regexp.Match(str, b)
	if err != nil {
		panic(err)
	}
	return isValid
}

func verifyCsr(csr models.NebulaCsr, hostname string, option int) (int, models.ApiError) {
	if csr.Hostname != hostname {
		log.Printf("Unhautorized. The hostname in the URL and the one in the Nebula CSR are different.\n")
		return http.StatusUnauthorized, models.ApiError{Code: 403, Message: "Unhautorized. The hostname in the URL and the one in the Nebula CSR are different."}
	}
	if option != RENROLL && csr.Rekey {
		log.Println("Bad Request. Rekey is true.")
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Rekey is true"}
	}

	switch option {
	case ENROLL:
		if csr.ServerKeygen {
			log.Println("Bad Request. ServerKeygen is true.")
			return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. ServerKeygen is true. If you wanted to enroll with a server keygen, please visit https://" + Service_ip + ":" + Service_port + "/" + "/ncsr/" + hostname + "/serverkeygen"}
		}
	case SERVERKEYGEN:
		if !csr.ServerKeygen {
			log.Println("Bad Request. ServerKeygen is false.")
			return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. ServerKeygen is false. If you wanted to enroll with a client-generated nebula public key, please visit https://" + Service_ip + ":" + Service_port + "/" + "/ncsr/" + hostname + "/enroll"}
		}
		return 0, models.ApiError{}
	case RENROLL:
		if !csr.Rekey || (csr.Rekey && csr.ServerKeygen) {
			return 0, models.ApiError{}
		}
	}

	if len(csr.PublicKey) == 0 {
		log.Println("Bad Request. Public key is not provided.")
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Public key is not provided"}
	}
	if len(csr.Pop) == 0 {
		log.Println("Bad Request. Proof of Possession is not provided.")
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Proof of Possession is not provided"}
	}

	var csr_ver = models.RawNebulaCsr{
		ServerKeygen: &csr.ServerKeygen,
		Rekey:        &csr.Rekey,
		Hostname:     csr.Hostname,
		PublicKey:    csr.PublicKey,
	}

	b, err := proto.Marshal(&csr_ver)

	if err != nil {
		log.Fatalln("Failed to encode Nebula CSR:", err)
		return http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()}
	}

	if !ed25519.Verify(csr.PublicKey, b, csr.Pop) {
		log.Println("Bad Request. Proof of Possession is not valid.")
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Proof of Possession is not valid"}
	}
	return 0, models.ApiError{}
}

func sendCSR(csr *models.NebulaCsr, option int) (*models.CaResponse, error) {
	var path string
	switch option {
	case ENROLL:
		path = "/ncsr/sign"
	case RENROLL:
		if csr.ServerKeygen {
			path = "/ncsr/generate"
		} else {
			path = "/ncsr/sign"
		}
	case SERVERKEYGEN:
		path = "/ncsr/generate"
	}

	raw_csr := models.RawNebulaCsr{
		ServerKeygen: &csr.ServerKeygen,
		Rekey:        &csr.Rekey,
		Hostname:     csr.Hostname,
		PublicKey:    csr.PublicKey,
		Pop:          csr.Pop,
		Groups:       csr.Groups,
	}

	b, err := protojson.Marshal(&raw_csr)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post("http://"+Ca_service_ip+":"+Ca_service_port+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}

	b, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var error_response *models.ApiError
	if json.Unmarshal(b, error_response) != nil {
		return nil, error_response
	}
	var response *models.CaResponse
	err = json.Unmarshal(b, response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func getCSRResponse(hostname string, csr *models.NebulaCsr, option int) (*models.NebulaCsrResponse, error) {
	var conf_resp *models.ConfResponse
	var err error
	if option != RENROLL {
		conf_resp, err = requestConf(hostname)
		if err != nil {
			return nil, err
		}
	}

	csr.Groups = conf_resp.Groups
	ca_response, err := sendCSR(csr, ENROLL)
	if err != nil {
		return nil, err
	}

	var csr_resp models.NebulaCsrResponse
	csr_resp.NebulaCert = ca_response.NebulaCert
	if option == SERVERKEYGEN {
		csr_resp.NebulaPrivateKey = ca_response.NebulaPrivateKey
	}
	if option != RENROLL {
		csr_resp.NebulaConf = conf_resp.NebulaConf
	}

	file, err := os.OpenFile("ncsr/"+hostname, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Could not write to file: %v", err)
	}
	defer file.Close()

	file.WriteString(string(models.COMPLETED) + "\n")
	file.WriteString(ca_response.NebulaCert.Details.NotAfter.String())
	return &csr_resp, nil
}

func requestConf(hostname string) (*models.ConfResponse, error) {
	resp, err := http.Get("http://" + Conf_service_ip + ":" + Conf_service_port + "/configs/" + hostname)
	if err != nil {
		return nil, err
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var error_response *models.ApiError
	if json.Unmarshal(b, error_response) != nil {
		return nil, error_response
	}
	var response *models.ConfResponse
	err = json.Unmarshal(b, &response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func Enroll(c *gin.Context) {
	logF, err := os.OpenFile(Log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening file: %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	log.SetOutput(logF)
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	defer logF.Close()

	log.Println("Nebula Enroll request received")

	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		log.Printf("Bad request: %v. No hostname provided\n", err)
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		log.Fatalf("Error opening /ncsr/"+hostname+": %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); !isPending {
		log.Println("The hostname " + hostname + " has already enrolled")
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has already enrolled. If you want to re-enroll, please visit https:https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		log.Printf("Bad request: %v. No Nebula Certificate Signing Request provided\n", err)
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, ENROLL)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, ENROLL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		log.Printf("Internal Server Error: %v\n", err)
		return
	}
	c.JSON(http.StatusOK, csr_resp)
}

func NcsrApplication(c *gin.Context) {
	logF, err := os.OpenFile(Log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening file: %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	defer logF.Close()

	log.SetOutput(logF)
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	log.Println("Nebula CSR Application received")

	var hostname string = ""
	if err := c.ShouldBindJSON(&hostname); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		log.Printf("Bad request: %v. No hostname provided\n", err)
		return
	}

	if _, err := os.Stat("ncsr/" + hostname); err == nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Conflict. A Nebula CSR for the hostname you provided already exists. If you want to re-enroll, please visit https://nebula_est/ncsr/" + hostname + "/reenroll"})
		log.Printf("Conflict: %v. Conflict. A Nebula CSR for the provided hostname already exists.\n", err)
		return
	}

	if !isValidHostname(hostname, Hostnames_file) {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: The hostname you provided was not found in the Configuration service list"})
		log.Printf("Bad request: %v. Hostname not found in Config service list\n", err)
		return
	}

	if _, err := os.Stat("ncsr/" + hostname); err == nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Conflict. A Nebula CSR for the hostname you provided already exists. If you want to re-enroll, please visit https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		log.Printf("Conflict: %v. A Nebula CSR for the provided hostname already exists.\n", err)
		return
	}

	applicationFile, err := os.OpenFile("ncsr/"+hostname, os.O_CREATE|os.O_WRONLY, 0600)
	if err == nil {
		log.Fatalf("Error creating /ncsr/"+hostname+": %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}
	if _, err := applicationFile.WriteString(string(models.PENDING)); err != nil {
		log.Fatalf("Could not write "+string(models.PENDING)+" status to  /ncsr/"+hostname+": %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	c.Header("Location", "http://"+Service_ip+":"+Service_port+"/ncsr/"+hostname)
	c.Status(http.StatusCreated)
}

func NcsrStatus(c *gin.Context) {
	logF, err := os.OpenFile(Log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening file: %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	defer logF.Close()

	log.SetOutput(logF)
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		log.Printf("Bad request: %v. No hostname provided\n", err)
		return
	}

	log.Println("Nebula CSR Status request received for hostname: " + hostname)

	file, err := os.OpenFile("ncsr/"+hostname, os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ApiError{Code: 404, Message: "Not found. Could not find an open Nebula CSR application for the specified hostname. If you want to enroll, provide your hostname to http:" + Service_ip + ":" + Service_port + "/ncsr"})
		log.Printf("Not found: %v Could not find an open Nebula CSR application for %s. If you want to enroll, provide your hostname to http:%s:%s/ncsr", err, hostname, Service_ip, Service_port)
		return
	}
	defer file.Close()

	fileScanner := bufio.NewScanner(file)

	fileScanner.Split(bufio.ScanLines)
	var fileLines []string
	for fileScanner.Scan() {
		fileLines = append(fileLines, fileScanner.Text())
	}

	if len(fileLines) == 2 {
		notAfter, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", fileLines[1])
		if err != nil {
			log.Fatalf("Error parsing notAfter field in file: %v\n", err)
			c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
			panic(err)
		}
		if time.Until(notAfter) < 0 {
			fileLines[0] = string(models.EXPIRED)
			for _, s := range fileLines {
				file.WriteString(s + "\n")
			}
		}
	}
	c.JSON(http.StatusOK, fileLines[0])
}

func Reenroll(c *gin.Context) {
	logF, err := os.OpenFile(Log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening file: %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	defer logF.Close()

	log.SetOutput(logF)
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	log.Println("Nebula Re-enroll request received")

	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		log.Printf("Bad request: %v. No hostname provided\n", err)
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		log.Fatalf("Error opening /ncsr/"+hostname+": %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); isPending {
		log.Println("The hostname " + hostname + " has not yet finished the first enrollment phase, cannot re-enroll")
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has not yet finished enrolling. If you want to do so, please visit https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/enroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		log.Printf("Bad request: %v. No Nebula Certificate Signing Request provided\n", err)
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, RENROLL)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, RENROLL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		log.Printf("Internal Server Error: %v\n", err)
		return
	}
	c.JSON(http.StatusOK, csr_resp)

}

func Serverkeygen(c *gin.Context) {
	logF, err := os.OpenFile(Log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error opening file: %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	defer logF.Close()

	log.SetOutput(logF)
	log.SetFlags(log.Lshortfile | log.LstdFlags)
	log.Println("Nebula Enroll request received")

	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		log.Printf("Bad request: %v. No hostname provided\n", err)
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		log.Fatalf("Error opening /ncsr/"+hostname+": %v\n", err)
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); !isPending {
		log.Println("The hostname " + hostname + " has already enrolled")
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has already enrolled. If you want to re-enroll, please visit https:https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		log.Printf("Bad request: %v. No Nebula Certificate Signing Request provided\n", err)
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, SERVERKEYGEN)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, SERVERKEYGEN)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		log.Printf("Internal Server Error: %v\n", err)
		return
	}
	c.JSON(http.StatusOK, csr_resp)
}
