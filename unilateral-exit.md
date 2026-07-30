# Salida unilateral

Verificado el 2026-07-27 contra `arkade-os/arkd` (`pkg/ark-lib/script`) y `@arkade-os/sdk` 0.4.51,
y contrastado con el equipo de Arkade.

## El diseño

Dos piezas separadas, y la clave está en no confundirlas:

```
Hoja de exit:  CSV + 2-de-2 (hedgePk, longPk)         <- dentro de Ark, N-de-N obligatorio
Destino:       2-de-3 {hedgePk, longPk, servicePk}    <- Bitcoin normal, sin restricciones
```

La hoja de salida vive dentro de la VTXO, así que tiene que ser una forma que arkd sepa decodificar
— y todas son N-de-N. El **destino** del barrido, en cambio, es *"any Bitcoin Output Script"*: una
vez el UTXO está en cadena y ha vencido el CSV, arkd no pinta nada. Ahí un 2-de-3 con umbral real
es legal y trivial.

## La transacción se pre-firma en el funding

Esta es la pieza que hace que todo encaje. En el momento de crear el contrato, con las dos partes
presentes y cooperando, se firma ya la transacción de salida:

```
input:   la VTXO, gastada por la hoja de exit (nSequence = CSV)
output:  el 2-de-3 {hedge, long, service}
firmas:  hedge + long, ambas recogidas ahora
```

A partir de ahí **cualquiera de las dos partes puede difundirla sola** cuando venza el CSV, sin
necesitar nada de la otra. No hay que coordinar firmas en el momento de la emergencia, que es
justo cuando la contraparte tiene incentivo para no cooperar.

## Qué cubre

| Riesgo | Cómo queda cubierto |
|---|---|
| Que una parte robe el colateral de la otra | La hoja es 2-de-2 y la única tx firmada va al 2-de-3. Nadie puede desviar el destino |
| Que una parte desaparezca y bloquee la salida | La tx ya está firmada; la otra la difunde sola |
| Que una parte desaparezca tras la salida | En el 2-de-3, la otra parte + el servicio mueven los fondos |

El servicio nunca custodia: es una firma de tres, y no puede mover nada solo.

## Consecuencia: el reparto sale del covenant

Cuando los fondos aterrizan en el 2-de-3, **el covenant ya no reparte**. El split entre hedge y
long lo resuelven los firmantes del vault — por acuerdo, o con el servicio arbitrando según el
precio del oráculo.

Es un debilitamiento real, pero es inherente a cualquier salida unilateral: un exit siempre tira el
covenant. La alternativa (dejar que cualquiera barra a donde quiera) es estrictamente peor.

## Herramienta de recuperación

Lo que hay que construir, y que el SDK no da hecho:

- Generar y firmar el paquete de salida en el funding
- Persistirlo en los tres lados (hedge, long, servicio)
- Difundirlo y esperar el CSV — aquí sí sirve `UnilateralExit.Executor`, que es agnóstico al
  contrato (*"never parses transaction hex — it only relays it"*) y sólo necesita Esplora
- Gestionar el gasto posterior desde el 2-de-3

## Por qué no un 2-de-3 en la propia hoja

Descartado, y por dos motivos independientes:

1. **Consenso.** `OP_CHECKMULTISIG` está deshabilitado en tapscript (BIP342: *"behave in the same
   way as OP_RETURN, by failing and terminating the script immediately"*). El sustituto es
   `OP_CHECKSIGADD`.
2. **arkd.** `MultisigClosure` es siempre N-de-N: el decoder exige que el entero empujado iguale el
   número de claves, y luego reconstruye el script y compara byte a byte. Además `DecodeClosure`
   (`closure.go:31`) es una lista blanca cerrada de 5 formas — arkd no *verifica* la hoja, la
   **clasifica**, para poder calcular exit delays, forfeits y barridos.

El SDK te deja construir la hoja igualmente (`new VtxoScript(scripts)` sólo comprueba que el árbol
taproot se ensambla), pero al registrarla `ParseVtxoScript` falla en duro y te quedas con un
taproot normal en L1, sin Ark encima.

---

*De dónde venía la confusión: dos capas distintas. m-de-n es tapscript válido en Bitcoin, así que
es natural asumir que vale como hoja de Ark — pero arkd necesita entender cada hoja, no sólo
verificarla. Y al revés: el 2-de-3 que queríamos sí existe, simplemente vive en el destino del
barrido, no en la hoja. El vocabulario tampoco ayuda — en los ejemplos de Arkade "2-2" significa
"las dos partes", no un umbral de 2.*
