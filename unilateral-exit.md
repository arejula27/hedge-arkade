# Por qué la salida unilateral no puede ser un 2-de-3 en una sola hoja

Verificado el 2026-07-27 contra `arkade-os/arkd` (`pkg/ark-lib/script`),
`arkade-os/compiler` @ `3988a9d`, `arkade-os/emulator` @ `1359823` y `@arkade-os/sdk` 0.4.51.

---

## Resumen

| Pregunta | Respuesta |
|---|---|
| ¿Puedo hacer un exit 2-de-3 en **una hoja**? | **No.** arkd sólo sabe decodificar closures N-de-N |
| ¿Puedo tener un exit 2-de-3 **en el contrato**? | **Sí.** Tres hojas de 2-de-2. arkd las soporta explícitamente |
| ¿Hay helper en el SDK para ejecutar el exit? | **Sí.** `UnilateralExit.estimate / prepare / Executor` |
| ¿Escribo el contrato a mano o con el compilador? | **A mano**, pero no por esto — ver §4 |

---

## 1. Por qué una hoja no puede ser 2-de-3

Una hoja de exit en Ark es un `CSVMultisigClosure`, que embebe un `MultisigClosure`
(`arkd/pkg/ark-lib/script/closure.go:335`). arkd tiene que **decodificar** cada closure para
calcular delays de exit y barridos, y su decoder rechaza cualquier cosa que no sea N-de-N:

```go
// closure.go, MultisigClosure.decodeChecksigAdd
if tokenizer.Err() != nil || len(pubkeys) != txscript.AsSmallInt(tokenizer.Opcode()) {
    return false, nil
}
```

El entero empujado antes de `OP_NUMEQUAL` **tiene que ser igual al número de claves**. Un
`<pk1> CHECKSIGADD <pk2> CHECKSIGADD <pk3> CHECKSIGADD OP_2 NUMEQUAL` falla ahí: 3 claves ≠ 2.

Y después hay una segunda barrera — el decoder reconstruye el script y exige igualdad byte a byte:

```go
rebuilt, err := f.Script()
if !bytes.Equal(rebuilt, script) {
    return false, nil
}
```

Como `MultisigClosure.Script()` sólo sabe generar N-de-N, ningún m-de-n sobrevive el round-trip.
No es una limitación del compilador ni del SDK: **es el formato que arkd sabe leer**.

Las otras dos capas simplemente lo respetan:

- **SDK** (`chunk-EFNLTS6Q.js:170`): `p2tr_ms(params.pubkeys.length, params.pubkeys)` — el umbral
  *es* el número de claves. `MultisigTapscript.Params` no tiene campo de threshold.
- **Compilador** (`src/compiler/tapscript.rs:147`), que además documenta el porqué:

  > arkd's MultisigClosure is **always N-of-N** (the decoder requires the pushed integer to equal
  > the key count, `closure.go:172`). So a declared threshold must equal the key count; anything
  > less cannot decode.

  Con test que lo fija (`tapscript.rs:704`):

  ```rust
  #[test]
  fn threshold_below_keycount_is_shape_error() {
      // 3 claves, threshold 2
      let err = validate_closure_shape(&c, "t").unwrap_err();
      assert!(err.contains("N-of-N"));
  }
  ```

**Alcance de la restricción**: el call site (`tapscript.rs:501`) itera sólo sobre
`contract.tapscripts`. Aplica **únicamente a funciones `tapscript`**, es decir a hojas L1. Dentro
de un covenant sí hay m-de-n — `threshold_oracle.ark` cuenta firmas en un bucle con
`checkSigFromStack` y comprueba `valid >= threshold`. Pero un exit es por definición `tapscript`
(si dependiera del emulador no serviría para nada cuando el emulador está caído), así que la
restricción pega justo donde nos duele.

---

## 2. La construcción que sí funciona: tres hojas de 2-de-2

El árbol taproot es un **OR** entre hojas. Un 2-de-3 sobre `{hedge, long, servicio}` es
exactamente la unión de sus tres parejas:

```
Leaf 3a:  older(exit) + checkMultisig([hedgePk, servicePk], [...])   // 2-de-2
Leaf 3b:  older(exit) + checkMultisig([longPk,  servicePk], [...])   // 2-de-2
Leaf 3c:  older(exit) + checkMultisig([hedgePk, longPk],    [...])   // 2-de-2
```

Cada hoja es N-de-N, así que cada una decodifica. Cualquier pareja de las tres puede salir; ninguna
parte puede salir sola. Eso *es* un 2-de-3.

arkd soporta múltiples hojas de exit de forma explícita — no es un truco que se le cuele.
`vtxo_script.go:167` itera sobre todas y se queda con la más corta:

```go
func (v *TapscriptsVtxoScript) SmallestExitDelay() (*arklib.RelativeLocktime, error) {
    var smallest *arklib.RelativeLocktime
    for _, closure := range v.ExitClosures() {   // <- plural, itera todas
        ...
        if smallest == nil || closureExitLocktime.LessThan(*smallest) {
```

Y `ExitClosures()` recoge todo `CSVMultisigClosure` o `ConditionCSVMultisigClosure` del script
(`vtxo_script.go:215`).

**Consecuencia de diseño**: como arkd usa el delay *más pequeño* para sus cálculos, las tres hojas
deben llevar el mismo CSV. Si una fuera más corta, definiría el exit delay efectivo de toda la
VTXO.

---

## 3. Sí hay helper para ejecutar el exit

`@arkade-os/sdk` exporta `UnilateralExit`:

```ts
declare const UnilateralExit: {
    readonly estimate: typeof estimate;   // cotiza coste (nº de txs, fees) sin tocar fondos
    readonly prepare:  typeof prepare;    // firma todas las txs y difunde el splitter de fees
    readonly Executor: typeof Executor;   // lleva el paquete a término, sólo necesita Esplora
};
```

De su propia documentación:

> `prepare` signs every transaction needed to land the VTXOs onchain and broadcasts the
> fee-funding splitter; `Executor` drives the resulting package to completion with nothing but an
> Esplora-compatible endpoint — **no keys, no Arkade infrastructure**.

Es decir: el paquete de salida se pre-firma mientras todo va bien, y luego lo puede ejecutar
cualquiera (una watchtower) sin claves. Encaja bien con nuestro caso — pero **hay que verificarlo
contra una hoja de 2-de-2**, porque todos los contratos de ejemplo del compilador usan exits de
firma única y no está confirmado que `prepare` sepa recoger dos firmas. Pendiente de probar.

También existe `Unroll` para desplegar la rama del árbol de batch, y
`serializeExitPackage` / `deserializeExitPackage` para persistir el paquete.

---

## 4. Compilador vs. a mano

La razón para escribir el contrato a mano **no** es el 2-de-3. El compilador expresa las tres
hojas 2-de-2 sin problema: `checkMultisig([a, b], [sigA, sigB])` es N-de-N y es legal. De hecho
`htlc.ark:19` ya usa esa forma.

La razón real es la deriva compilador/VM, y está **confirmada en ambos extremos**:

- El compilador sigue emitiendo la familia de 64 bits — `src/opcodes/mod.rs:82-85` define
  `OP_ADD64`, `OP_SUB64`, `OP_MUL64`, `OP_DIV64`, y `src/compiler/comparison.rs:220` emite
  `OP_GREATERTHAN64`.
- La VM ya no los tiene: cero ocurrencias de esa familia en `emulator/pkg/arkade/opcode.go`. Fue
  unificada a BigNum — ahora hay `OP_MUL = 0x95` con `opcodeMul` operando sobre BigNums
  (`opcode.go:194`, `:493`, `:2394`).

Un contrato con aritmética compilado con `arkadec` **no ejecuta en la VM actual**. Y este contrato
es aritmética casi de principio a fin.

Por tanto: el contrato se escribe como objeto `Program` del SDK, y los `.ark` se mantienen como
spec legible. Las hojas de exit son la parte que menos sufre (no llevan aritmética), pero por
coherencia van por el mismo camino.

---

## 5. Por qué no basta con un exit de firma única

Todos los contratos de ejemplo del compilador (`fuji_safe`, `cash_secured_put`, `stability_vault`,
`bond_mint`, `token_vault`...) tienen exits de firma única:

```
function unilateral(signature seekerSig) tapscript {
  require(older(exit));
  require(checkSig(seekerSig, seekerPk));
}
```

Es correcto para contratos de **un solo dueño**. Aquí no vale: un exit tira el covenant, y con él
toda la fórmula de reparto. Quien ejecute un exit de firma única se lleva `totalCollateral`
entero, incluido el colateral de la otra parte.

`stability_vault.ark:351` tiene exactamente ese agujero: pasado el CSV, el seeker puede salir solo
y quedarse con todo lo del provider. Es la misma superficie de soft-custody que la documentación
de Arkade critica en `repayment_pool.ark`.

De ahí el quórum de dos. No es paranoia: es la única forma de que la salida de emergencia no sea
un camino de robo.
