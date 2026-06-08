// Package cli es el área de cobertura del BINARIO `cmd/quark` dentro del arnés
// de aceptación (superapp S9).
//
// El CLI es superficie pública de Quark (se entrega desde v1.1.0) pero NO encaja
// en el gate de manifiesto de símbolos (S3): `cmd/quark` es `package main`, y su
// contrato público es la interfaz de COMANDOS/flags de cobra, no símbolos Go
// exportados. Por eso se cubre con un mecanismo distinto, paralelo al gate de
// símbolos pero a nivel comando:
//
//	denominador  = árbol de comandos cobra (enumerable de `Use:`)
//	numerador    = comandos ejercidos (build del binario → exec → assert)
//	gate         = exit-code + golden output esperados; allowlist para lo diferido
//
// Estado: el smoke (`cli_smoke_test.go`, build tag `superapp_cli`) prueba el
// MECANISMO contra SQLite — build de `cmd/quark`, y ejercicio de un subconjunto
// representativo (help / inspect / validate / migrate) con asserts de exit-code
// y salida. El manifiesto-de-comandos completo, el golden output por comando y la
// matriz cross-engine son S9 propio (ver `HANDOFF.md`). El smoke loguea
// explícitamente qué comandos quedan SIN cubrir (anti "silent cap").
package cli
