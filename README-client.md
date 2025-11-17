# Client Preview

Spin up a local instance of [chatgpt-next-web](https://github.com/Yidadaa/ChatGPT-Next-Web) pointing at your private-llm endpoint. Export `OPENAI_API_KEY` and `OPENAI_API_URL` from the generated Secret before running the container.

```sh
docker run --rm \
  -p 3000:3000 \
  --add-host=private-llm.msp:host-gateway \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e BASE_URL="$OPENAI_API_URL" \
  -e HIDE_USER_API_KEY=1 \
  -e DISABLE_FAST_LINK=1 \
  -e DEFAULT_MODEL='/models/tinyllama.gguf' \
  -e CUSTOM_MODELS='-all,+/models/tinyllama.gguf' \
  yidadaa/chatgpt-next-web:latest
```

Open `http://localhost:3000` in your browser. The UI proxies all requests to the provided `BASE_URL` and works best when the operator is exposing the `llminstance-sample` Service via Traefik.