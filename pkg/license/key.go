package license

// ServerURL is the CNW License Server endpoint. Hard-coded — no config or env
// override. Letting operators redirect this would be a license bypass vector
// (point at a fake server that always returns plan=enterprise). Change here
// requires a code release.
const ServerURL = "https://license-api.cloudnativeworks.com"
