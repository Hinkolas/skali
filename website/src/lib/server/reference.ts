// The manifest reference page's content: the same file skali skill read
// serves, taken from the release the site follows rather than from main,
// because main may already document fields the released binaries reject.
// It is rendered once, when the page is prerendered.
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { Marked, type Tokens } from 'marked';

const root = fileURLToPath(new URL('../../../../', import.meta.url));

/** The reference's path in the repository. */
export const manifestReferencePath = 'internal/skill/assets/reference/manifest.md';

export type Heading = { id: string; text: string; depth: number };

export type Reference = {
	/** The document's own title, taken out of the body. */
	title: string;
	html: string;
	/** The second- and third-level headings, in order. */
	headings: Heading[];
};

/** Reads the manifest reference at a git ref and renders it. */
export function manifestReference(ref: string): Reference {
	let source: string;
	try {
		source = execFileSync('git', ['show', `${ref}:${manifestReferencePath}`], {
			cwd: root,
			encoding: 'utf8',
			stdio: ['ignore', 'pipe', 'pipe']
		});
	} catch {
		throw new Error(`${ref} has no ${manifestReferencePath}`);
	}
	return render(source);
}

function render(source: string): Reference {
	const headings: Heading[] = [];
	const used = new Set<string>();
	let title = '';

	const marked = new Marked({
		renderer: {
			heading({ tokens, depth, text }) {
				const inner = this.parser.parseInline(tokens);
				if (depth === 1) {
					title = text;
					return '';
				}
				const id = uniqueSlug(text, used);
				if (depth <= 3) headings.push({ id, text: plain(text), depth });
				return `<h${depth} id="${id}">${inner}</h${depth}>\n`;
			},
			code({ text, lang }: Tokens.Code) {
				const [language, marker] = (lang ?? '').split(/\s+/);
				const body = language === 'yaml' ? highlightYaml(text) : escape(text);
				// "yaml manifest" fences are complete manifests, compiled in CI;
				// fragments are excerpts and carry no file name.
				const file = marker === 'manifest' ? ' data-file="skali.yaml"' : '';
				return `<pre${file}><code>${body}</code></pre>\n`;
			}
		}
	});

	const html = marked.parse(source, { async: false });
	if (!title) throw new Error(`${manifestReferencePath} has no title`);
	return { title, html, headings };
}

function plain(text: string): string {
	return text.replaceAll('`', '').replaceAll('**', '');
}

function uniqueSlug(text: string, used: Set<string>): string {
	const base =
		plain(text)
			.toLowerCase()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-|-$/g, '') || 'section';
	let slug = base;
	for (let n = 2; used.has(slug); n++) slug = `${base}-${n}`;
	used.add(slug);
	return slug;
}

function escape(text: string): string {
	return text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
}

// Keys a step up the text ramp, comments a step down: comments carry the
// defaults and allowed values, so they read as notes beside the YAML.
function highlightYaml(source: string): string {
	return source
		.split('\n')
		.map((line) => {
			const at = commentStart(line);
			const code = at < 0 ? line : line.slice(0, at);
			const comment = at < 0 ? '' : line.slice(at);
			const key = /^(\s*(?:-\s+)?)([A-Za-z_$][\w.$-]*)(:)(?=\s|$)/.exec(code);
			let html = key
				? `${escape(key[1])}<span class="yaml-key">${escape(key[2])}</span>${key[3]}${escape(code.slice(key[0].length))}`
				: escape(code);
			if (comment) html += `<span class="yaml-comment">${escape(comment)}</span>`;
			return html;
		})
		.join('\n');
}

// Where a line's comment starts: a # at the line start or after
// whitespace, outside quotes. -1 when there is none.
function commentStart(line: string): number {
	let quote = '';
	for (let i = 0; i < line.length; i++) {
		const c = line[i];
		if (quote) {
			if (c === quote) quote = '';
		} else if (c === '"' || c === "'") {
			quote = c;
		} else if (c === '#' && (i === 0 || /\s/.test(line[i - 1]))) {
			return i;
		}
	}
	return -1;
}
