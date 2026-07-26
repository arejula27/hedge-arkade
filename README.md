# Arkade Hedge Contract — Spec (inspirado en AnyHedge de BCH)

## Objetivo

Portar el protocolo AnyHedge (Bitcoin Cash, en producción desde 2020, ~$4.9M TVL vía BCH Bull)
a Arkade/Ark, aprovechando la VM de introspección + aritmética del emulador de Arkade para la
lógica de negocio, y resolviendo la salida unilateral (que en Ark cae a Bitcoin L1 puro, sin esos
opcodes) con un camino de emergencia separado.

Referencia original: `anyhedge.cash`
(https://gitlab.com/GeneralProtocols/anyhedge/library/-/blob/development/lib/anyhedge.cash)

---

## Estado y decisiones tomadas

| Decisión | Valor | Fecha |
|---|---|---|
| Producto | **Plazo fijo** (AnyHedge), no perpetuo | 2026-07-27 |
| Base del contrato | `stability_vault.ark` del repo `arkade-os/compiler` | 2026-07-27 |
| Oráculo | **Operado por nosotros**, formato Fuji/stability | 2026-07-27 |
| Salida de emergencia | Tres hojas 2-de-2 (= 2-de-3) | 2026-07-27 |
| Lenguaje | TypeScript (contrato + servicio) + arnés de tests en Go | 2026-07-27 |
| Funding rate | **Descartado** — es lo que hace perpetuo a stability | 2026-07-27 |

**Viabilidad: confirmada opcode a opcode.** Todo lo que la spec necesita existe en la VM del
emulador y resuelve desde el SDK de TypeScript. Ver §Viabilidad verificada.

---

## Punto de partida: `stability_vault.ark`

`../compiler/examples/stability/stability_vault.ark` (355 líneas) es este mismo contrato con
otros nombres. No lo reescribimos desde cero — lo adaptamos.

| Esta spec | stability_vault |
|---|---|
| Hedge (short 1x, valor USD fijo) | `seeker` — "holds a fixed USD value" |
| Short/Long (colateral extra, long apalancado) | `provider` — "leveraged BTC long" |
| `hedgePayoutInBtc = hedgeValue / endPrice` | `seekerRaw = newTargetUSD * 100000000 / oraclePrice` |
| `output_hedge + output_cp == totalLockedBtc` | `providerPayout = totalCollateral - seekerRaw` |
| `lowLiquidationPrice` | `if (seekerRaw >= totalCollateral)` → hedge se lleva todo |
| `highLiquidationPrice` | `if (seekerRaw <= 0)` → contraparte se lleva todo |

Los umbrales de liquidación de AnyHedge y el clamp de stability son la misma cosa: la condición
`seekerRaw >= totalCollateral` **es** el precio de liquidación bajo, calculado en vez de
precomputado. Esto elimina la necesidad de derivar los umbrales del leverage por separado.

**Qué hay que añadir sobre stability**: `maturityTime` (stability es perpetuo), cierre mutuo
anticipado, y las hojas de salida multi-parte.

**Qué hay que quitar**: `fundingRatePerSec` y toda la acumulación asociada (`lastUpdate`,
`elapsed`, `delta`). Un contrato a plazo fijo no paga funding; el precio de la cobertura se paga
por fuera al abrir la posición.

---

## Actores

- **Hedge**: quiere preservar el valor de su BTC en términos de un activo externo (USD, oro...).
  Posición equivalente a "short 1x".
- **Long**: contraparte especuladora, apuesta en la dirección opuesta del precio. Aporta el
  colateral extra que garantiza el payout del Hedge.
- **Oráculo**: firma mensajes de precio periódicamente. Operado por nosotros. No conoce la
  existencia de ningún contrato concreto.
- **Servicio**: coordina, construye la transacción de liquidación y aparece como tercera clave en
  las hojas de emergencia. No custodia fondos ni decide el reparto — sólo ejecuta la fórmula.
- **Emulador (ASP)**: co-firma la liquidación si el Arkade Script pasa. No es Bitcoin consensus.

---

## Parámetros del contrato (fijados al crear)

| Parámetro | Tipo | Descripción |
|---|---|---|
| `hedgePk` | pubkey | Clave del lado Hedge |
| `longPk` | pubkey | Clave del lado Long |
| `servicePk` | pubkey | Clave del servicio (sólo para hojas de emergencia) |
| `oraclePk` | pubkey | Clave pública del oráculo de precio |
| `ticker` | bytes32 | Identificador del feed, p.ej. `sha256("BTC/USD")` |
| `hedgeValueCents` | int | Valor que el Hedge protege, en céntimos de USD. Constante |
| `totalCollateral` | int | Suma de los aportes de ambas partes, en sats. Constante |
| `maturityTime` | int | Unix seconds. Vencimiento normal |
| `exit` | int | Delay CSV de las hojas de emergencia (segundos, múltiplo de 512) |

`hedgeLeverage` es 1x por definición. El leverage del Long es implícito en el ratio
`totalCollateral / hedgeValueCents` — no es un parámetro del contrato, es una consecuencia de
cuánto aporta cada lado al fondear.

---

## Fórmulas

Sin funding rate, `hedgeValueCents` es constante, así que la matemática de liquidación colapsa a:

```
hedgePayoutSats = clamp(hedgeValueCents * 1e8 / oraclePrice, 0, totalCollateral)
longPayoutSats  = totalCollateral - hedgePayoutSats
```

Unidades: `hedgeValueCents` [céntimos] × `1e8` [sats/BTC] ÷ `oraclePrice` [céntimos/BTC] = [sats].
`OP_DIV` trunca, y el truncamiento va siempre en contra de quien construye la transacción.

**Invariante de conservación** (clave para simplificar la introspección):
el total de BTC bloqueado nunca cambia, sólo se redistribuye entre las dos partes.

```
output_hedge.value + output_long.value == totalCollateral
```

Por tanto el covenant sólo necesita verificar:
1. `output_hedge.value >= hedgePayoutSats` (calculado con la firma del oráculo)
2. `output_long.value >= totalCollateral - hedgePayoutSats` (el resto, sin recalcular)

Con salvaguarda de dust: si un payout cae por debajo de 330 sats, se omite la salida
(mismo patrón que `stability_vault.ark:294`).

---

## Formato del mensaje del oráculo

Adoptamos tal cual el formato Fuji/stability (`stability_vault.ark:22-28`), porque operamos el
oráculo nosotros y este formato ya está probado a través del compilador:

```
msg = sha256(ticker || price || timestamp)
sig = sign(oraclePk, msg)
```

- `price` y `timestamp`: enteros **8 bytes little-endian unsigned**
- `price` en céntimos de USD por BTC
- `ticker` permite añadir feeds sin tocar el contrato

Comprobaciones de frescura en el covenant:

```
oracleAge = tx.offchainTime - oracleTime
require(oracleAge >= 0,   "future-dated oracle")   // rechaza precios futuros
require(oracleAge <= 600, "stale oracle")          // ventana de 10 minutos
```

Verificación con `checkSigFromStack(oracleSig, oraclePk, oracleMsg)` — `OP_CHECKSIGFROMSTACK`
(`0xcc`), firma compacta de 64 bytes, pubkey Schnorr x-only de 32 bytes.

---

## Estructura Taproot

Internal key = **NUMS** (Nothing Up My Sleeve) — sin key-path spendable, fuerza a usar una de las
ramas del árbol.

```
                    Taproot output (NUMS internal key)
                                |
   ┌────────────┬───────────────┼───────────────┬──────────────┐
   |            |               |               |              |
 Leaf 1      Leaf 2          Leaf 3a         Leaf 3b        Leaf 3c
Liquidación  Cierre mutuo   CSV 2-de-2      CSV 2-de-2     CSV 2-de-2
/vencimiento  (2-de-2)     hedge+servicio   long+servicio  hedge+long
(covenant)
```

### Leaf 1 — Liquidación / vencimiento
- Covenant completo, co-firmado por el emulador
- `checkSigFromStack` valida el mensaje de precio firmado por el oráculo
- `MUL`/`DIV` calculan `hedgePayoutSats`
- `INSPECTOUTPUTVALUE` fuerza la conservación
- Trigger: `tx.offchainTime >= maturityTime` **o** `hedgePayoutSats >= totalCollateral`
  (liquidación anticipada — el Long se quedó sin colateral)
- Normalmente **no se transmite a Bitcoin** — se resuelve dentro de una ronda de Arkade

### Leaf 2 — Cierre mutuo anticipado
- 2-de-2 entre Hedge y Long
- Sin oráculo, sin emulador — reparto acordado directamente por ambas partes
- Ejecutable directamente en Bitcoin L1 (sólo firmas, sin introspección)

### Leaf 3a/3b/3c — Salida de emergencia (2-de-3 entre Hedge, Long y Servicio)

Ark exige una salida unilateral que no dependa del emulador. Como el covenant se cae en esa
salida, **no puede ser de firma única**: quien la ejecutase se llevaría todo el colateral,
incluido el de la otra parte. Por eso exigimos quórum de dos.

Un 2-de-3 **no se puede escribir en una sola hoja**. El validador del compilador
(`../compiler/src/compiler/tapscript.rs:147`) lo rechaza citando a arkd:

> arkd's MultisigClosure is **always N-of-N** (the decoder requires the pushed integer to equal
> the key count, `closure.go:172`). So a declared threshold must equal the key count; anything
> less cannot decode.

Cubierto por el test `threshold_below_keycount_is_shape_error` (`tapscript.rs:704`). La
restricción aplica **sólo a funciones `tapscript`** — dentro de un covenant sí hay m-de-n
(ver `threshold_oracle.ark`), pero un exit es por definición `tapscript`.

Como el árbol taproot es un OR, el 2-de-3 se escribe como **tres hojas de 2-de-2**, todas con el
mismo delay CSV (arkd usa el más pequeño de todos para calcular el exit delay de la VTXO, así que
una hoja más corta definiría el de todas). Leaf 3c coincide funcionalmente con Leaf 2 salvo por el
timelock.

Análisis completo, con las fuentes en arkd, el SDK y el compilador: **`unilateral-exit.md`**.

Riesgo conocido y aceptado: colusión Servicio + una de las partes. Mitigación: el servicio firma
de forma determinista según el último precio firmado por el oráculo, nunca a discreción manual;
cada liquidación queda acompañada de la firma del oráculo como prueba pública auditable.

> **Nota**: todos los contratos de ejemplo del compilador (`fuji_safe`, `cash_secured_put`,
> `stability_vault`, `bond_mint`...) usan un exit de firma única. Es correcto para contratos de
> un solo dueño; aquí hay dos partes con dinero dentro, y un exit single-sig sería una superficie
> de robo — la misma crítica que la documentación de Arkade hace a `repayment_pool.ark`.

---

## Viabilidad verificada

Comprobado contra `arkade-os/emulator` (`pkg/arkade`), `@arkade-os/sdk` 0.4.51 y
`arkade-os/compiler` @ `3988a9d`.

| Necesidad | Opcode | Origen |
|---|---|---|
| `hedgeValue / endPrice` | `MUL` (0x95), `DIV` (0x96), `MOD` (0x97) | Legacy reactivados, vía el enum `OP` de `@scure/btc-signer` |
| Precio firmado por oráculo | `CHECKSIGFROMSTACK` (0xcc) | `ARKADE_OP` |
| Reconstruir el mensaje | `CAT`, `SUBSTR`, `NUM2BIN`, `BIN2NUM` | Mixto |
| Conservación de valor | `INSPECTOUTPUTVALUE` (0xcf) | `ARKADE_OP` |
| Umbrales | `GREATERTHANOREQUAL`, `LESSTHAN` | Bitcoin base |
| Vencimiento | CLTV en el tapscript | — |

Todos resuelven vía `ARKADE_OPS = { ...OP, ...ARKADE_OP }` en el SDK.

BigNum es de precisión arbitraria hasta 520 bytes. El análisis de error numérico publicado de
AnyHedge existe porque BCH trabajaba con enteros de 64 bits; aquí no hay techo de overflow. Queda
el truncamiento de `DIV`, que se gestiona con escala fija a 1e8 igual que stability.

---

## Stack

- **Contrato**: objeto `Program` del SDK de TypeScript (`@arkade-os/sdk`)
- **Servicio web**: TypeScript / Node.js 22
- **Tests unitarios de aritmética**: `vitest`
- **Tests del covenant contra la VM real**: Go, contra `github.com/arkade-os/emulator/pkg/arkade`
- **Integración**: nigiri + arkd + arkd-wallet + emulator vía Docker Compose

`arkadec` (el `.ark`) se usa como **spec legible**, no está en el build path — ver AGENTS.md
§Toolchain.

---

## Diferencias clave vs. AnyHedge (BCH)

| | BCH (AnyHedge) | Arkade |
|---|---|---|
| Dónde corre el covenant rico | Consenso de nodos BCH, on-chain, siempre | VM del emulador, off-chain, mientras el ASP coopera |
| Camino de emergencia | No existe — el camino rico ya es la capa final | Leaf 3a/3b/3c, necesario porque Bitcoin L1 no valida introspección |
| Velocidad de liquidación | Requiere confirmación en bloque BCH | Instantánea, off-chain (salvo Leaf 3) |
| Seguridad de base | Hashrate/seguridad de BCH | Hereda seguridad de Bitcoin L1 |
| Confianza requerida en camino normal | Ninguna — el nodo valida | Honestidad del ASP (mitigada por Leaf 3 como red de seguridad) |
| Umbrales de liquidación | Precomputados desde el leverage | Implícitos en el clamp |

---

## Pendiente de definir

- [ ] **Emparejamiento**: ¿bilateral (las dos partes se conocen al crear) o order book en el
      servicio? Decide si el servicio es un co-firmante sin estado o una pieza con estado
- [ ] **Umbral de liquidación alto**: con `hedgeLeverage = 1x` y la formulación por conservación,
      parece que sólo liga el umbral bajo (`hedgePayoutSats >= totalCollateral`). Confirmar que el
      alto de AnyHedge sólo hace falta si el lado hedge va apalancado
- [ ] **Ventana del CSV** en las hojas de emergencia (segundos, múltiplo de 512)
- [ ] **Fee del servicio/protocolo** (AnyHedge la cobra incluida trustlessly en el contrato)
- [ ] **Ratio mínimo de colateral** al fondear, para que el Long no abra posiciones ya liquidables
- [ ] Alcance del servicio web: ¿sólo API, o también UI?
