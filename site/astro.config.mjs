// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLlmsTxt from 'starlight-llms-txt';

// Project GitHub Pages: https://muthuishere.github.io/workwire
// https://astro.build/config
export default defineConfig({
	site: 'https://muthuishere.github.io',
	base: '/workwire',
	integrations: [
		starlight({
			title: 'workwire',
			// Remove the right-hand "On this page" table of contents site-wide.
			tableOfContents: false,
			description:
				'An open-source, HTTP-only message hub for the work between workers: agents and humans discover each other, ask questions, and answer from their own live context. One Go binary, plain HTTP, no broker, no SDK.',
			plugins: [
				starlightLlmsTxt({
					projectName: 'workwire',
					description:
						'A tiny, LLM-free, HTTP-only message hub where AI agents and the humans working with them are peers: auto-registration via an installed agent skill, long-poll inbox delivery into already-running sessions, read-time thread context, and a plainly served A2A v0.3.0 surface — one static Go binary, no broker, no SDK.',
					details:
						'workwire is dumb plumbing on purpose: it stores envelopes, serves them back with hub-assigned cursors and read-time thread context, and keeps a dynamic agent registry. The intelligence is the agent sessions themselves — a singleton listener delivers inbound questions into the running session, which answers from its own live context. Anything that speaks the three HTTP surfaces (register, envelopes, A2A card/ask) is a peer.',
				}),
			],
			customCss: ['@fontsource-variable/inter', './src/styles/deemwar.css'],
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/muthuishere/workwire' },
			],
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'Quickstart', slug: 'quickstart' },
						{ label: 'The 60-second tour', slug: 'tour' },
					],
				},
				{
					label: 'Concepts',
					items: [{ label: 'The work between workers', slug: 'concepts' }],
				},
				{
					label: 'Scenarios',
					items: [
						{ label: 'Ask a running session', slug: 'scenarios/ask-a-running-session' },
						{ label: 'Two agents disagree', slug: 'scenarios/two-agents-disagree' },
						{ label: 'A human decides', slug: 'scenarios/a-human-decides' },
						{
							label: 'Targeted discussion with groups',
							slug: 'scenarios/targeted-discussion-with-groups',
						},
						{
							label: 'Onboard a peer',
							slug: 'scenarios/onboard-a-peer-with-agents-md',
						},
						{ label: 'An external client', slug: 'scenarios/an-external-client' },
					],
				},
				{
					label: "How it's different",
					items: [
						{ label: 'vs MCP', slug: 'vs-mcp' },
						{ label: 'vs Agent Skills', slug: 'vs-skills' },
						{ label: 'vs A2A', slug: 'vs-a2a' },
						{ label: 'The landscape', slug: 'landscape' },
					],
				},
				{
					label: 'Guide',
					items: [
						{ label: 'CLI reference', slug: 'cli' },
						{ label: 'HTTP API', slug: 'http-api' },
						{ label: 'The agent skill', slug: 'agent-skill' },
						{ label: 'Run it anywhere', slug: 'deploy' },
					],
				},
				{
					label: 'Design',
					items: [{ label: 'ADRs & specs', slug: 'references' }],
				},
			],
		}),
	],
});
