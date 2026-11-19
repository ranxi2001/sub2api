# OpenAI-compatible video upstreams

The OpenAI Videos account capability relays asynchronous video tasks to API Key
upstreams implementing this contract:

| Operation | Method and path |
| --- | --- |
| Create | POST /v1/videos |
| Get status | GET /v1/videos/{id} |
| Download | GET /v1/videos/{id}/content |

1. Create an OpenAI **API Key** account. Set its Base URL and upstream key, and
   select **OpenAI Videos** under **Endpoint capabilities**. The capability is
   off by default. Use the root Base URL if the same account also has
   **Seedance (Ark)** enabled.
2. Add the account to a group with media generation permission (**Allow
   image generation**). Set model mappings and configure the model's price in
   the channel linked to the group. Use per-request pricing for a fixed price
   or video pricing for a per-second price. A composite group can route a
   model to OpenAI using its existing model routing.
3. Call the gateway with a key for that group. Send the format the upstream
   supports (JSON or multipart form data). Supply a model and an explicit
   positive integer duration in seconds. A compatible upstream can use
   duration as the field name. Per-second settlement uses the requested
   duration and the existing resolution tiers.

Example JSON body:

    {"model":"video-model","prompt":"Slow waves at sunset","seconds":5,"size":"1280x720"}

The gateway returns its own task ID. Use that ID and the original gateway key
for both subsequent requests. The gateway contacts the account that created the
task. A completed status or a successful video download triggers settlement
once, using the existing video billing flow. A task that the client never
queries or downloads is not observed for settlement. Submission timeouts should
be checked upstream before retrying, since the upstream may have accepted the
job.
