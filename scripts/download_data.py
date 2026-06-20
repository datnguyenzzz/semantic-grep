"""
Download benchmark datasets and save them locally 

Usage:
  python3 download_data.py all
"""

import os
import subprocess
import sys

import numpy as np

DATA_DIR = os.path.expanduser("data")


def download_openai(dim):
    from datasets import load_dataset

    path = os.path.join(DATA_DIR, f"openai-{dim}.npy")
    if os.path.exists(path):
        print(f"Already downloaded: {path}")
        return
    os.makedirs(DATA_DIR, exist_ok=True)
    name = f"Qdrant/dbpedia-entities-openai3-text-embedding-3-large-{dim}-1M"
    col = f"text-embedding-3-large-{dim}-embedding"
    print(f"Downloading {name}...")
    ds = load_dataset(name, split="train")
    ds.set_format("numpy")
    vecs = np.array(ds[col], dtype=np.float32)
    np.save(path, vecs)
    print(f"Saved: {path} ({os.path.getsize(path) / 1024 / 1024:.0f} MB)")


TARGETS = {
    "openai-1536": lambda: download_openai(1536),
    "openai-3072": lambda: download_openai(3072),
}

if __name__ == "__main__":
    args = sys.argv[1:] if len(sys.argv) > 1 else ["all"]
    if "all" in args:
        args = list(TARGETS.keys())

    for name in args:
        if name not in TARGETS:
            print(f"Unknown dataset: {name}")
            print(f"Available: {', '.join(TARGETS.keys())}")
            continue
        TARGETS[name]()