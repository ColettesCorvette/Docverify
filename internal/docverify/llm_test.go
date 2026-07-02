package docverify

import "testing"

func TestLLMSemaphoreDefaultMatchesProd(t *testing.T) {
	t.Setenv("LLM_MAX_CONCURRENT", "")
	InitLLMSemaphore()
	if cap(llmSemaphore) != 1 {
		t.Errorf("cap(llmSemaphore) = %d, attendu 1 (aligné prod)", cap(llmSemaphore))
	}
}
