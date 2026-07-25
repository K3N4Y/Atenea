// Package tool is the published contract a tool implements.
//
// Tool is the whole of it: name, description, input schema, execute. A host
// materializes the first three into what the model is told it may call, and
// settles Execute when the model calls it. The types around it — Call and
// Result — are the two ends of one settlement.
//
// What is NOT here is intentional: the registry, the output capping, the input
// repair pass and the tools atenea ships are private under internal/tool. A
// third party implements this interface; it does not reach into the turn loop.
package tool
