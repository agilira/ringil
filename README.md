# ringil

L'app ringil: CLI, `serve` (canali, trigger, API) e, in arrivo, la TUI. Usa
[ringil-agent-sdk](https://github.com/agilira/ringil-agent-sdk); le garanzie di sicurezza
sono quelle dell'SDK ([SECURITY.md](https://github.com/agilira/ringil-agent-sdk/blob/main/SECURITY.md)).

Finché l'SDK non ha un tag, l'app lo trova accanto a sé: clona i due repo nella stessa
cartella (`ringil` e `ringil-agent-sdk`).

## CLI

```sh
make chat                                   # chat con le chiavi di ENV_FILE (default ../ringil-agent-sdk/.env)
./ringil run "che ore sono a Tokyo?"
./ringil -S lavoro chat          # sessione salvata: la riapri e riprende
./ringil sessions                # elenco; "sessions rm <nome>" per cancellare
./ringil run "trova dove è definito X"   # tool sui file nella cartella di lancio;
                                                   # per altre cartelle l'agente chiede il permesso
./ringil sandbox --mount ~/dati -- python3 script.py               # esecuzione isolata
```

Flag principali: `--provider`, `--model`, `--instructions`, `--max-turns`, `--max-cost`,
`--workflow-cost`, `--allow-http`, `--no-tools`, `--session`/`-S`, `--sessions-dir`,
`--config`/`-c`.

Il costo in dollari di ogni richiesta compare nella riga di fine turno. I prezzi vengono dal
listino pubblico di OpenRouter (tutti i vendor), scaricato una volta al giorno in
`<data>/ringil/prices.json`; OpenRouter dichiara il costo di ogni chiamata, che vince; i
modelli su questa macchina e l'abbonamento ChatGPT costano zero. `prices:` in ringil.yaml
aggiunge o corregge un modello. `--max-cost` e `--workflow-cost` su un modello senza prezzo
sono rifiutati, perché non fermerebbero nulla.

### ringil.yaml

Tutta la configurazione sta in un file, `<config>/ringil/ringil.yaml` (o `--config`):
impostazioni, profili, server MCP, agenti remoti e `serve`. Le chiavi sono i nomi dei
flag con `_` al posto di `-`; un flag vince sul file per quel comando, e
`RINGIL_PROVIDER`, `RINGIL_MODEL`, `RINGIL_EMBED_MODEL` nell'ambiente pure. I segreti
non ci stanno mai: `key_env` e `${NOME}` dicono quale variabile li contiene. Il file
dev'essere tuo e non scrivibile da altri; i percorsi relativi delle istruzioni e di
`sessions_dir` partono dalla sua cartella. Esempio completo: `docs/ringil.example.yaml`.

Il modello si sceglie per nome del provider (`provider:`, `--provider`, `RINGIL_PROVIDER`).
Pronti senza configurazione, con il segreto della chiave: `anthropic` (`ANTHROPIC_API_KEY`),
`chatgpt` (abbonamento, `ringil login chatgpt`), `openai` (`OPENAI_API_KEY`), `google`
(Gemini, `GEMINI_API_KEY`), `openrouter`, `nvidia` (NIM), `deepseek`, `requesty`, `zai`,
`moonshot`, `xai` (Grok), `mistral` (`<NOME>_API_KEY`), `ollama` (questa macchina, senza chiave). Altri, quanti se ne
vuole, nella sezione `providers:` con un nome, l'indirizzo e il segreto della chiave; una
voce con il nome di uno pronto ne cambia indirizzo o segreto. Profili e modelli audio
scelgono il loro provider per nome. Ogni chiave va solo al suo provider.

Una chiave sconosciuta è un errore, non un default silenzioso. Il file, i file di
istruzioni che nomina e le skill installate si leggono insieme, come una revisione
(Argus): mentre gira, ringil segue le modifiche di tutti; una che passa i controlli
vale dalla prossima run (profili, istruzioni, impostazioni, chi parla con il bot), una
che non li passa è segnalata e resta la revisione precedente. Ogni run di `serve` usa
una sola revisione dall'inizio alla fine e il suo log ne riporta l'impronta
(`config=...`); ogni revisione finisce in `audit.db` (`audit_file`, `no_audit`) per
nome e hash, mai il testo: SQLite con la catena di hash di Argus, che scopre un record
cambiato o tolto (un file `.jsonl` è a righe, senza catena). Un percorso di istruzioni non può contenere `*`, `?` o `[`. Server MCP, agenti remoti e il resto di `serve` valgono al
prossimo avvio, e il log lo dice. Gli strumenti sui file dell'agente non toccano
`ringil.yaml` né i file di istruzioni senza chiedere.


## Sviluppo

```sh
make check          # fmt, vet, lint, sicurezza, test
make race
make fuzz FUZZTIME=5m
```

## Licenza

MPL-2.0.
