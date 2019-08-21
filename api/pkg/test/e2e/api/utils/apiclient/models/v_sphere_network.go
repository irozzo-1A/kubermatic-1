// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	strfmt "github.com/go-openapi/strfmt"

	"github.com/go-openapi/swag"
)

// VSphereNetwork VSphereNetwork is the object representing a vsphere network.
// swagger:model VSphereNetwork
type VSphereNetwork struct {

	// AbsolutePath is the absolute path inside vCenter
	AbsolutePath string `json:"absolutePath,omitempty"`

	// Name is the name of the network
	Name string `json:"name,omitempty"`

	// RelativePath is the relative path inside the datacenter
	RelativePath string `json:"relativePath,omitempty"`

	// Type defines the type of network
	Type string `json:"type,omitempty"`
}

// Validate validates this v sphere network
func (m *VSphereNetwork) Validate(formats strfmt.Registry) error {
	return nil
}

// MarshalBinary interface implementation
func (m *VSphereNetwork) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *VSphereNetwork) UnmarshalBinary(b []byte) error {
	var res VSphereNetwork
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}
