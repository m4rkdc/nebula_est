/*
 * Nebula Configuration service for NEST (Nebula Enrollment over Secure Transport) - OpenAPI 3.0
 *
 * This is a simple Nebula Configuration service that generates Nebula configuration files from Dhall configuration files on behalf of the NEST service
 *
 * API version: 0.2.1
 * Contact: gianmarco.decola@studio.unibo.it
 */
package models

type ConfResponse struct {
	NebulaConf []byte `json:"nebulaConf,omitempty"`

	Groups []string `json:"groups,omitempty"`

	Ip string `json:"ip,omitempty"`

	NebulaPath string `json:"NebulaPath"`
}
