document.getElementById('search-form').addEventListener('submit', async (e) => {
    e.preventDefault();

    const query = document.getElementById('query-input').value;
    const format = document.querySelector('input[name="format"]:checked').value;

    const loader = document.getElementById('loader');
    const resultsPanel = document.getElementById('results-panel');

    // Reset and show loader
    loader.classList.remove('hidden');
    resultsPanel.classList.add('hidden');

    try {
        const response = await fetch('/api/query', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify({ query, format })
        });

        if (!response.ok) {
            throw new Error(await response.text());
        }

        const data = await response.json();

        // Render response
        document.getElementById('generator-badge').textContent = `Model: ${data.generator}`;
        
        const answerBlock = document.getElementById('answer-content');
        
        if (format === 'table') {
            answerBlock.innerHTML = renderMarkdownTable(data.answer);
        } else if (format === 'json') {
            answerBlock.innerHTML = `<pre style="color: #60a5fa; font-family: monospace; font-size: 0.95rem; overflow-x: auto;">${JSON.stringify(tryParseJSON(data.answer), null, 2)}</pre>`;
        } else {
            answerBlock.textContent = data.answer;
        }

        document.getElementById('source-content').textContent = data.source;
        document.getElementById('score-content').textContent = Number(data.score).toFixed(4);

        // Hide loader, show results
        loader.classList.add('hidden');
        resultsPanel.classList.remove('hidden');

    } catch (err) {
        loader.classList.add('hidden');
        alert(`Error searching: ${err.message}`);
    }
});

function tryParseJSON(text) {
    try {
        const clean = text.replace(/```json/g, '').replace(/```/g, '').trim();
        return JSON.parse(clean);
    } catch {
        return { raw_response: text };
    }
}

// Basic markdown table parser for UI display
function renderMarkdownTable(markdown) {
    const lines = markdown.trim().split('\n');
    let html = '<table>';
    let inTable = false;

    lines.forEach((line) => {
        if (line.includes('|')) {
            const cols = line.split('|').map(c => c.trim()).filter((c, i, a) => i > 0 && i < a.length - 1);
            if (cols.length === 0) return;
            
            // Skip separators like |---|---|
            if (line.includes('-')) return;

            inTable = true;
            html += '<tr>';
            cols.forEach(col => {
                html += `<td>${col}</td>`;
            });
            html += '</tr>';
        }
    });

    html += '</table>';
    return inTable ? html : `<div style="white-space: pre-wrap;">${markdown}</div>`;
}
