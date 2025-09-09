package authorization

import "fmt"

var (
	// ErrProjectAccessDenied is returned when user doesn't have access to the requested project
	ErrProjectAccessDenied = fmt.Errorf("access denied: you don't have permission to access this project")
	
	// ErrProjectNotFound is returned when the requested project doesn't exist
	ErrProjectNotFound = fmt.Errorf("project not found")
	
	// ErrInvalidProjectID is returned when the project ID format is invalid
	ErrInvalidProjectID = fmt.Errorf("invalid project ID format")
	
	// ErrEmptyProject is returned when no project is specified for a protected resource
	ErrEmptyProject = fmt.Errorf("project is required for this operation")
	
	// ErrInsufficientPrivileges is returned when user role doesn't have required privileges
	ErrInsufficientPrivileges = fmt.Errorf("insufficient privileges for this operation")
)