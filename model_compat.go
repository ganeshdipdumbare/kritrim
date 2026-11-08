package kritrim

// DeepSeek returns a DeepSeek model (openai-compatible). The key comes from
// DEEPSEEK_API_KEY.
func DeepSeek(model string, cfg ...ModelConfig) Model {
	return Compatible("deepseek", "https://api.deepseek.com/v1", model, cfg...)
}

// Groq returns a Groq model. The key comes from GROQ_API_KEY.
func Groq(model string, cfg ...ModelConfig) Model {
	return Compatible("groq", "https://api.groq.com/openai/v1", model, cfg...)
}

// Mistral returns a Mistral model. The key comes from MISTRAL_API_KEY.
func Mistral(model string, cfg ...ModelConfig) Model {
	return Compatible("mistral", "https://api.mistral.ai/v1", model, cfg...)
}

// OpenRouter returns an OpenRouter model. The key comes from
// OPENROUTER_API_KEY.
func OpenRouter(model string, cfg ...ModelConfig) Model {
	return Compatible("openrouter", "https://openrouter.ai/api/v1", model, cfg...)
}

// LiteLLM returns a model served by a LiteLLM gateway at baseURL.
func LiteLLM(baseURL, model string, cfg ...ModelConfig) Model {
	return Compatible("litellm", baseURL, model, cfg...)
}
