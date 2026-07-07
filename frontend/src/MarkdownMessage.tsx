import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'

// rehype-highlight's `languages` option defaults to lowlight's "common"
// bundle (37 languages, not its ~190-language "all" bundle) — that default
// already covers everything this workbench's assistant output realistically
// contains (bash, go, json, markdown, typescript, yaml, diff, ...), so it's
// left unset rather than hand-rolled. highlight.js has no distinct "tsx"
// grammar, so ```tsx fences are aliased to the typescript highlighter.
const highlightOptions = { aliases: { typescript: ['tsx'] } }

// MarkdownMessage renders LLM-authored message content as formatted
// markdown (headings, lists, tables, fenced code with syntax
// highlighting) instead of a raw wall of text. Deliberately does not use
// rehype-raw: literal HTML tags in content stay escaped text rather than
// becoming real DOM, since assistant output is only semi-trusted — do not
// add it without a real XSS review.
export function MarkdownMessage({ content }: { content: string }) {
  return (
    <div className="markdown-message">
      <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[[rehypeHighlight, highlightOptions]]}>
        {content}
      </ReactMarkdown>
    </div>
  )
}
