#!/usr/bin/env python3
"""Recall benchmark: OpenAI d=1536 & 3072, 4-bit."""
import os, json, time, numpy as np 
import faiss 

DATA_DIR = os.path.expanduser("data")
RESULTS_DIR = os.path.join(os.path.dirname(__file__), "..", "results")
DIM = 3072
BIT_WIDTH = 4
K = 64
K_VALUES = [1, 2, 4, 8, 16, 32]
SEED = 42


def load_openai(dim):
    path = os.path.join(DATA_DIR, f"openai-{dim}.npy")
    all_vecs = np.load(path)
    rng = np.random.RandomState(SEED)
    idx = rng.permutation(len(all_vecs))
    database = all_vecs[idx[:100_000]].astype(np.float32)
    queries = all_vecs[idx[100_000:101_000]].astype(np.float32)
    database /= np.linalg.norm(database, axis=-1, keepdims=True)
    queries /= np.linalg.norm(queries, axis=-1, keepdims=True)
    return database, queries


def recall_at_1_at_k(true_top1, predicted_indices, k):
    return float(np.mean([true_top1[i] in predicted_indices[i, :k] for i in range(len(true_top1))]))


def main():
    dimensions = [1536, 3072]
    for dim in dimensions:
        print(f"\n=== OpenAI d={dim} {BIT_WIDTH}-bit (seed={SEED}) ===")
        
        # Verify that the npy file exists before loading
        npy_path = os.path.join(DATA_DIR, f"openai-{dim}.npy")
        if not os.path.exists(npy_path):
            print(f"  ⚠️  Dataset file not found at {npy_path}. Skipping.")
            continue
            
        m = dim // 2
        nbits = 8

        database, queries = load_openai(dim)
        true_top1 = np.argmax(queries @ database.T, axis=1)

        t0 = time.time()
        index_faiss = faiss.IndexPQ(dim, m, nbits, faiss.METRIC_INNER_PRODUCT)
        index_faiss.train(database)
        index_faiss.add(database)
        _, faiss_ids = index_faiss.search(queries, K)
        faiss_recalls = {str(k): round(recall_at_1_at_k(true_top1, faiss_ids, k), 4) for k in K_VALUES}
        print(f"  FAISS ({time.time() - t0:.1f}s) recall@1 = {faiss_recalls['1']:.4f}")

        results = {
            "dataset": f"openai-{dim}",
            "dim": dim,
            "bit_width": BIT_WIDTH,
            "faiss_variant": f"IndexPQ(m={m}, nbits={nbits})",
            "seed": SEED,
            "faiss_recalls": faiss_recalls,
        }

        print("FAISS recalls:", faiss_recalls)

        os.makedirs(RESULTS_DIR, exist_ok=True)
        out_path = os.path.join(RESULTS_DIR, f"faiss_recall_d{dim}_4bit.json")
        with open(out_path, "w") as f:
            json.dump(results, f, indent=2)
        print(f"Saved to {out_path}")


if __name__ == "__main__":
    main()