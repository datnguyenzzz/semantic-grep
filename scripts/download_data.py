#!/usr/bin/env python3
"""Unified dataset downloader: downloads aligned raw text and pre-computed embeddings."""
import os
import json
import time
import numpy as np

DATA_DIR = os.path.expanduser("data")

def download_aligned(dim, limit=101000):
    from datasets import load_dataset

    text_path = os.path.join(DATA_DIR, f"dbpedia_text_d{dim}.json")
    npy_path = os.path.join(DATA_DIR, f"openai-{dim}.npy")

    if os.path.exists(text_path) and os.path.exists(npy_path):
        print(f"✓ Already downloaded and aligned for d={dim}: {text_path} & {npy_path}")
        return

    os.makedirs(DATA_DIR, exist_ok=True)
    name = f"Qdrant/dbpedia-entities-openai3-text-embedding-3-large-{dim}-1M"
    col = f"text-embedding-3-large-{dim}-embedding"
    
    print(f"\n📥 Downloading aligned dataset from {name}...")
    ds = load_dataset(name, split="train")
    
    num_entities = min(limit, len(ds))
    print(f"⚙ Aligning and extracting first {num_entities} entries...")

    records = []
    embeddings = []

    t0 = time.time()
    for i in range(num_entities):
        row = ds[i]
        
        # 1. Extract raw text
        records.append({
            "id": row.get("_id", f"doc_{i}"),
            "title": row.get("title", ""),
            "text": row.get("text", "")
        })
        
        # 2. Extract corresponding embedding in perfect sync
        vec = row.get(col)
        embeddings.append(vec)
        
        if (i + 1) % 20000 == 0 or (i + 1) == num_entities:
            print(f"  • Aligned progress: {i + 1}/{num_entities} entries...")

    # Save raw text to JSON
    with open(text_path, "w", encoding="utf-8") as f:
        json.dump(records, f, indent=2, ensure_ascii=False)
    print(f"✓ Saved text data: {text_path} ({os.path.getsize(text_path) / 1024 / 1024:.1f} MB)")

    # Save embeddings to NumPy array
    vecs = np.array(embeddings, dtype=np.float32)
    np.save(npy_path, vecs)
    print(f"✓ Saved embeddings: {npy_path} ({os.path.getsize(npy_path) / 1024 / 1024:.1f} MB)")
    print(f"✓ Done d={dim} in {time.time() - t0:.1f} seconds!")

def main():
    # Download aligned datasets for both 1536 and 3072 dimensions
    download_aligned(1536, 100_000)
    download_aligned(3072, 50_000)

if __name__ == "__main__":
    main()
