package service

// Usage/model/keepalive regression controls need semantic output independently
// of their usage snapshots. This helper adds only synthetic content after the
// initial frame; it never adds usage or a terminal to an EOF-negative fixture.
const anthropicFixtureTextSSE = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"fixture\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
