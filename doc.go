// Package clicontract turns one declaration of a CLI's command surface into
// everything downstream consumers read: the cobra command tree, the
// machine-readable schema document, and the run orchestration that routes
// caller errors and exit codes.
//
// See the README for the declaration and adapter walkthrough.
package clicontract
