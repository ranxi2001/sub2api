# Model Pricing Data

This directory contains a local copy of the mirrored model pricing data as a fallback mechanism.

## Source
The original file is maintained by the LiteLLM project and mirrored into the `price-mirror` branch of this repository via GitHub Actions:
- Mirror branch (configurable via `PRICE_MIRROR_REPO`): https://raw.githubusercontent.com/<your-repo>/price-mirror/model_prices_and_context_window.json
- Upstream source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

## Purpose
This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process
The pricingService will:
1. First attempt to download the latest version from GitHub
2. If download fails, use this local copy as fallback
3. Log a warning when using the fallback file

## Manual Update
To manually update this file with the latest pricing data (if automation is unavailable):
```bash
curl -s https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json -o model_prices_and_context_window.json
```

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities

## OpenAI short-context update (2026-11-18)

The Standard short-context text rates were checked against the [OpenAI pricing page](https://developers.openai.com/api/docs/pricing?context-length=short). Prices below are USD per 1M tokens; JSON stores USD per token.

| Model | Input | Cached input | Output | 30m cache write | 12h cache write |
| --- | ---: | ---: | ---: | ---: | ---: |
| gpt-6.2-astra | 8 | 0.40 | 40 | 10 | 20 |
| gpt-6.2-sol | 2 | 0.10 | 10 | 2.50 | 5 |
| gpt-6-astra | 10 | 1 | 50 | 12.50 | 25 |
| gpt-6.1-sol | 2 | 0.10 | 10 | 2.50 | 5 |
| gpt-6.1-terra | 1 | 0.05 | 5 | 1.25 | 2.50 |
| gpt-6-sol | 2 | 0.20 | 10 | 2.50 | 5 |
| gpt-6-luna | 0.10 | 0.01 | 0.50 | 0.125 | 0.25 |
| gpt-5.6-sol | 4 | 0.40 | 20 | 5 | 10 |
| gpt-5.6-terra | 2 | 0.20 | 12 | 2.50 | 5 |
| gpt-5.6-luna | 0.20 | 0.02 | 1.20 | 0.25 | 0.50 |

The owner explicitly maps `gpt-6.2-astra-pro` to the same prices as `gpt-6.2-astra`; the official page does not list Pro independently. The GPT-5.6 Sol rates are promotional through November 30, 2026. Recheck the official page before updating prices after that date; this snapshot does not invent a future price or schedule a switch.

The bundled data and built-in defaults agree on these Standard rates, including separate cache-write TTLs. Existing GPT-5.6 Sol Fast, Flex and Batch columns are updated with their corresponding official rates. GPT-6 Astra continues to use its existing built-in fallback when absent from the bundled file. The remaining models in the flagship Standard text table already match the bundled input, cached-input and output rates.

The subsequent policy alignment follows Wei-Shaw/sub2api `2af3f1e15c98c893a89255a037f551046883b8c0`: GPT-6.2 Sol/Astra use 3x UltraFast before 2027-03-01 08:00 UTC and 6x thereafter, selected by request time. Their bundled and built-in cards apply input/cache 2x and output 1.5x above 272,000 input tokens. OpenAI OAuth/setup-token requests for GPT-6.2 Sol/Astra and GPT-6.1 Sol/Terra are exempt from model long-context surcharges; API keys are not. The fork-only Astra Pro ID retains its existing policy because upstream does not define one. Exact remote catalog entries, operator override files, group/channel prices and customer multipliers retain their existing precedence. Updating this file alone does not replace a running deployment's remote catalog or configuration.
