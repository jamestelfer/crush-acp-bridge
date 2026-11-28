# Changelog

## 0.1.0 (2026-11-28)


### Features

* align release pipeline, installer, and documentation ([#37](https://github.com/jamestelfer/crush-acp-bridge/issues/37)) ([daf5ef8](https://github.com/jamestelfer/crush-acp-bridge/commit/daf5ef88150cf2244013f62b3cfd5af0354e27e6))
* answer questions through a handler with a chat fallback ([#54](https://github.com/jamestelfer/crush-acp-bridge/issues/54)) ([7da36ce](https://github.com/jamestelfer/crush-acp-bridge/commit/7da36cef49e1db50aadae450801073b54dd854f0))
* ask Crush's questions in the editor ([#30](https://github.com/jamestelfer/crush-acp-bridge/issues/30)) ([80b14f2](https://github.com/jamestelfer/crush-acp-bridge/commit/80b14f24365621d7f7cfcaa38c254224ae17f57b))
* ask form-capable editors the whole question batch as one form ([#56](https://github.com/jamestelfer/crush-acp-bridge/issues/56)) ([7cafebc](https://github.com/jamestelfer/crush-acp-bridge/commit/7cafebc2e0fa62670179d4669e2e7a0ff6ccee7d))
* build elicitation forms, guard credentials and map responses ([#55](https://github.com/jamestelfer/crush-acp-bridge/issues/55)) ([4cd4267](https://github.com/jamestelfer/crush-acp-bridge/commit/4cd426727647746f678dba1e77bf19b0ee0838d2))
* decode question confirm text and resolution, add answer notes ([#53](https://github.com/jamestelfer/crush-acp-bridge/issues/53)) ([b95416d](https://github.com/jamestelfer/crush-acp-bridge/commit/b95416d982ae38563653918809260ecb29ff013a))
* initial project from go-project ([a3dc082](https://github.com/jamestelfer/crush-acp-bridge/commit/a3dc0820cfbb55fe6eec6b415b04826e1d93ece5))
* keep a turn alive across an event-stream drop ([#28](https://github.com/jamestelfer/crush-acp-bridge/issues/28)) ([f431a5b](https://github.com/jamestelfer/crush-acp-bridge/commit/f431a5b4c1df95d396733eb1d4afc04527309fdf))
* report missing provider credentials clearly ([#32](https://github.com/jamestelfer/crush-acp-bridge/issues/32)) ([dfac9cd](https://github.com/jamestelfer/crush-acp-bridge/commit/dfac9cd0a5d464dd59b5e7ede19cf42c95a3f4d0))
* run Crush as an ACP agent through Crush's shared server ([#18](https://github.com/jamestelfer/crush-acp-bridge/issues/18)) ([1f608ff](https://github.com/jamestelfer/crush-acp-bridge/commit/1f608ffc5d825d0a971c0056bef66ad07e34daf0))
* send image and embedded-resource prompts to Crush as attachments ([#29](https://github.com/jamestelfer/crush-acp-bridge/issues/29)) ([fa3abdb](https://github.com/jamestelfer/crush-acp-bridge/commit/fa3abdbe756351db741b87e9799ca9d22a148f4f))


### Bug Fixes

* delete the empty sessions editor probes leave in Crush ([#26](https://github.com/jamestelfer/crush-acp-bridge/issues/26)) ([f0dd1d0](https://github.com/jamestelfer/crush-acp-bridge/commit/f0dd1d05124d765e6603a42f8dafe3963276c625))
* **deps:** update actions/setup-go action to v7.1.0 ([#1](https://github.com/jamestelfer/crush-acp-bridge/issues/1)) ([2e2ab1e](https://github.com/jamestelfer/crush-acp-bridge/commit/2e2ab1e71870a041c80476dfb2761349101b2dd8))
* **deps:** update dependency goreleaser to v2.20.3 ([#35](https://github.com/jamestelfer/crush-acp-bridge/issues/35)) ([5eb6cd0](https://github.com/jamestelfer/crush-acp-bridge/commit/5eb6cd05246d7a2eabf9e92570b3ae4056a7a31b))
* **deps:** update go dependencies ([#19](https://github.com/jamestelfer/crush-acp-bridge/issues/19)) ([ffcac9d](https://github.com/jamestelfer/crush-acp-bridge/commit/ffcac9d741370d93c2f20a1c81fe784c60a3a72e))
* **deps:** update jdx/mise-action action to v5 ([#2](https://github.com/jamestelfer/crush-acp-bridge/issues/2)) ([23ac501](https://github.com/jamestelfer/crush-acp-bridge/commit/23ac5013097649bd64ff98f0c81df87feb5a91d7))
* **deps:** update jdx/mise-action action to v5.3.0 ([#49](https://github.com/jamestelfer/crush-acp-bridge/issues/49)) ([579afb1](https://github.com/jamestelfer/crush-acp-bridge/commit/579afb1982f2ebf8ee848bc7b61854c332c84777))
* **deps:** update module github.com/tidwall/gjson to v1.20.6 ([#48](https://github.com/jamestelfer/crush-acp-bridge/issues/48)) ([26dafbe](https://github.com/jamestelfer/crush-acp-bridge/commit/26dafbed36819ab53ed579416614c1ad3cef5be0))
* **deps:** update node.js to v26 ([#22](https://github.com/jamestelfer/crush-acp-bridge/issues/22)) ([5210e51](https://github.com/jamestelfer/crush-acp-bridge/commit/5210e51b824ec7bd0e57fadc6a88fc05a5c5cb49))
* refuse a Unix socket whose server runs as another user ([#21](https://github.com/jamestelfer/crush-acp-bridge/issues/21)) ([48a6a1c](https://github.com/jamestelfer/crush-acp-bridge/commit/48a6a1c9264fefef49f1f4277ab2e081fae2c2d1))
* remove a --managed server's cache directory once it exits ([#24](https://github.com/jamestelfer/crush-acp-bridge/issues/24)) ([66ad4ee](https://github.com/jamestelfer/crush-acp-bridge/commit/66ad4ee60aee7279f4de00ad6dd5e874182c1d48))
