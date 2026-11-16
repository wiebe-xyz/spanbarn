// Package repository is SpanBarn's query layer: every read and write of spans,
// traces, logs, metrics, prompts, projects and settings, routed through the
// write scheduler and labelled per operation. The files those queries run on
// are opened and managed by the engine subpackage; ForStorage builds a
// Repository over an engine.Storage.
package repository
