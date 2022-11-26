/*
 * Nebula Enrollment over Secure Transport - OpenAPI 3.0
 *
 * This is a simple Public Key Infrastructure Management Server based on the RFC7030 Enrollment over Secure Transport Protocol for a Nebula Mesh Network. The Service accepts requests from mutually authenticated TLS-PSK connections to create Nebula Certificates for the client, either by signing client-generated Nebula Public Keys or by generating Nebula key pairs and signing the server-generated Nebula public key and to create Nebula configuration files for the specific client. This Service acts as a Facade for the Nebula CA service (actually signign or creating the Nebula keys) and the Nebula Config service (actually creating the nebula Config. files).
 *
 * API version: 0.3.1
 * Contact: gianmarco.decola@studio.unibo.it
 */
package nest_service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/m4rkdc/nebula_est/pkg/models"
	"google.golang.org/protobuf/encoding/protojson"
)

// isValideHostname checks if the provided hostname is present in the Hostnames file
func isValidHostname(hostname string) (bool, error) {

	b, err := os.ReadFile(Hostnames_file)
	if err != nil {
		return false, err
	}

	isValid, err := regexp.Match(hostname, b)
	if err != nil {
		return false, err
	}
	return isValid, nil
}

func verifyCsr(csr models.NebulaCsr, hostname string, option int) (int, models.ApiError) {
	if csr.Hostname != hostname {
		return http.StatusUnauthorized, models.ApiError{Code: 403, Message: "Unhautorized. The hostname in the URL and the one in the Nebula CSR are different."}
	}
	if option != models.RENROLL && csr.Rekey {
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Rekey is true"}
	}

	switch option {
	case models.ENROLL:
		if csr.ServerKeygen {
			return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. ServerKeygen is true. If you wanted to enroll with a server keygen, please visit https://" + Service_ip + ":" + Service_port + "/" + "/ncsr/" + hostname + "/serverkeygen"}
		}
	case models.SERVERKEYGEN:
		if !csr.ServerKeygen {
			return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. ServerKeygen is false. If you wanted to enroll with a client-generated nebula public key, please visit https://" + Service_ip + ":" + Service_port + "/" + "/ncsr/" + hostname + "/enroll"}
		}
		return 0, models.ApiError{}
	case models.RENROLL:
		if !csr.Rekey || (csr.Rekey && csr.ServerKeygen) {
			return 0, models.ApiError{}
		}
	}

	if len(csr.PublicKey) == 0 {
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Public key is not provided"}
	}
	if len(csr.Pop) == 0 {
		return http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Proof of Possession is not provided"}
	}

	return 0, models.ApiError{}
}

func sendCSR(csr *models.NebulaCsr, option int) (*models.CaResponse, error) {
	var path string
	switch option {
	case models.ENROLL:
		path = "/ncsr/sign"
	case models.RENROLL:
		if csr.ServerKeygen {
			path = "/ncsr/generate"
		} else {
			path = "/ncsr/sign"
		}
	case models.SERVERKEYGEN:
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
	if option != models.RENROLL {
		conf_resp, err = requestConf(hostname)
		if err != nil {
			return nil, err
		}
	}

	csr.Groups = conf_resp.Groups
	ca_response, err := sendCSR(csr, models.ENROLL)
	if err != nil {
		return nil, err
	}

	var csr_resp models.NebulaCsrResponse
	csr_resp.NebulaCert = ca_response.NebulaCert
	if option == models.SERVERKEYGEN {
		csr_resp.NebulaPrivateKey = ca_response.NebulaPrivateKey
	}
	if option != models.RENROLL {
		csr_resp.NebulaConf = conf_resp.NebulaConf
	}

	file, err := os.OpenFile("ncsr/"+hostname, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		fmt.Printf("Could not write to file: %v\n", err)
		panic(err)
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
	fmt.Println("Nebula Enroll request received")

	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); !isPending {
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has already enrolled. If you want to re-enroll, please visit https:https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, models.ENROLL)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, models.ENROLL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, csr_resp)
}

func NcsrApplication(c *gin.Context) {
	fmt.Println("Nebula CSR Application received")

	var auth = models.NestAuth{}
	if err := c.ShouldBindJSON(&auth); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		return
	}

	if _, err := os.Stat("ncsr/" + auth.Hostname); err == nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Conflict. A Nebula CSR for the hostname you provided already exists. If you want to re-enroll, please visit https://nebula_est/ncsr/" + hostname + "/reenroll"})
		return
	}

	isValid, err := isValidHostname(auth.Hostname)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		return
	}
	if !isValid {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: The hostname you provided was not found in the Configuration service list"})
		return
	}

	if _, err := os.Stat("ncsr/" + auth.Hostname); err == nil {
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. A Nebula CSR for the hostname you provided already exists. If you want to re-enroll, please visit https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		return
	}

	key, err := os.ReadFile(HMAC_key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		return
	}

	if !Verify(auth.Hostname, key, auth.Secret) {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad Request. Could not succesfully verify the provided secret"})
		return
	}

	applicationFile, err := os.OpenFile("ncsr/"+auth.Hostname, os.O_CREATE|os.O_WRONLY, 0600)
	if err == nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}
	if _, err := applicationFile.WriteString(string(models.PENDING)); err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	c.Header("Location", "http://"+Service_ip+":"+Service_port+"/ncsr/"+auth.Hostname)
	c.Status(http.StatusCreated)
}

func NcsrStatus(c *gin.Context) {
	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		return
	}

	fmt.Println("Nebula CSR Status request received for hostname: " + hostname)

	file, err := os.OpenFile("ncsr/"+hostname, os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ApiError{Code: 404, Message: "Not found. Could not find an open Nebula CSR application for the specified hostname. If you want to enroll, provide your hostname to http:" + Service_ip + ":" + Service_port + "/ncsr"})
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
	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); isPending {
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has not yet finished enrolling. If you want to do so, please visit https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/enroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, models.RENROLL)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, models.RENROLL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, csr_resp)

}

func Serverkeygen(c *gin.Context) {
	fmt.Println("Nebula Enroll request received")

	hostname := c.Params.ByName("hostname")
	if hostname == "" {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no hostname provided"})
		return
	}

	b, err := os.ReadFile("ncsr/" + hostname)
	if err == nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal server error: " + err.Error()})
		panic(err)
	}

	if isPending, _ := regexp.Match(string(models.PENDING), b); !isPending {
		c.JSON(http.StatusConflict, models.ApiError{Code: 409, Message: "Conflict. This hostname has already enrolled. If you want to re-enroll, please visit https:https://" + Service_ip + ":" + Service_port + "/ncsr/" + hostname + "/reenroll"})
		return
	}

	var csr models.NebulaCsr

	if err := c.ShouldBindJSON(&csr); err != nil {
		c.JSON(http.StatusBadRequest, models.ApiError{Code: 400, Message: "Bad request: no Nebula Certificate Signing Request provided"})
		return
	}

	status_code, api_error := verifyCsr(csr, hostname, models.SERVERKEYGEN)
	if status_code != 0 {
		c.JSON(status_code, api_error)
		return
	}

	csr_resp, err := getCSRResponse(hostname, &csr, models.SERVERKEYGEN)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ApiError{Code: 500, Message: "Internal Server Error: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, csr_resp)
}
