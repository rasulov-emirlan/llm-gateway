#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-key}"
MODEL="${MODEL:-llama3.2}"

AUTH="-H \"Authorization: Bearer $API_KEY\""

green() { echo -e "\033[32m$1\033[0m"; }
red()   { echo -e "\033[31m$1\033[0m"; }
bold()  { echo -e "\033[1m$1\033[0m"; }

check() {
    if [ $? -eq 0 ]; then green "  PASS"; else red "  FAIL"; fi
}

bold "=== LLM Gateway E2E Tests ==="
echo ""

# 1. Health check
bold "1. Health check"
curl -s "$BASE_URL/health" | python3 -m json.tool
check
echo ""

# 2. List models
bold "2. List models"
curl -s -H "Authorization: Bearer $API_KEY" "$BASE_URL/v1/models" | python3 -m json.tool
check
echo ""

# 3. Chat completion (non-streaming)
bold "3. Chat completion (non-streaming)"
time curl -s -X POST "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"$MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in one word.\"}]
    }" | python3 -m json.tool
check
echo ""

# 4. Same request again (should be faster — cache hit)
bold "4. Same request (cache hit expected)"
time curl -s -X POST "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"$MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in one word.\"}]
    }" | python3 -m json.tool
check
echo ""

# 5. Streaming chat
bold "5. Streaming chat (SSE)"
curl -s -N -X POST "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"$MODEL\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Count to 3.\"}],
        \"stream\": true
    }"
echo ""
check
echo ""

# 6. Rate limiting test
bold "6. Rate limit test (25 rapid requests)"
SUCCESS=0
LIMITED=0
for i in $(seq 1 25); do
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"$MODEL\",
            \"messages\": [{\"role\": \"user\", \"content\": \"hi\"}]
        }")
    if [ "$STATUS" = "429" ]; then
        LIMITED=$((LIMITED + 1))
    else
        SUCCESS=$((SUCCESS + 1))
    fi
done
echo "  Successful: $SUCCESS, Rate limited: $LIMITED"
if [ "$LIMITED" -gt 0 ]; then
    green "  PASS — rate limiting works"
else
    red "  WARN — no 429s received (burst may be too high for this test)"
fi
echo ""

# 7. Fallback test (request non-existent model)
bold "7. Fallback test (non-existent model → should fallback)"
curl -s -X POST "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
        \"model\": \"nonexistent-model-xyz\",
        \"messages\": [{\"role\": \"user\", \"content\": \"Say yes.\"}]
    }" | python3 -m json.tool
check
echo ""

# 8. Auth test (no token)
bold "8. Auth test (should return 401)"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/v1/models")
if [ "$STATUS" = "401" ]; then
    green "  PASS — got 401 without token"
else
    red "  FAIL — expected 401, got $STATUS"
fi
echo ""

bold "=== Done ==="
