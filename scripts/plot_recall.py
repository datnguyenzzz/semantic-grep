#!/usr/bin/env python3
import os
import json
import matplotlib.pyplot as plt

RESULTS_DIR = os.path.join(os.path.dirname(__file__), "..", "results")

def plot_dimension(dim):
    faiss_path = os.path.join(RESULTS_DIR, f"faiss_recall_d{dim}_4bit.json")
    tq_path = os.path.join(RESULTS_DIR, f"tq_recall_d{dim}_4bit.json")

    faiss_data = {}
    if os.path.exists(faiss_path):
        with open(faiss_path, "r") as f:
            faiss_data = json.load(f).get("faiss_recalls", {})
    else:
        print(f"  ⚠️  FAISS result not found at: {faiss_path}")

    tq_data = {}
    if os.path.exists(tq_path):
        with open(tq_path, "r") as f:
            tq_data = json.load(f).get("tq_recalls", {})
    else:
        print(f"  ⚠️  TurboQuant result not found at: {tq_path}")

    if not faiss_data and not tq_data:
        print(f"⚠️  No recall data found for d={dim}. Skipping chart generation.")
        return

    # Sort k values numerically
    k_values = sorted([int(k) for k in faiss_data.keys()]) if faiss_data else sorted([int(k) for k in tq_data.keys()])
    
    faiss_y = [faiss_data.get(str(k), 0.0) for k in k_values] if faiss_data else []
    tq_y = [tq_data.get(str(k), 0.0) for k in k_values] if tq_data else []

    plt.figure(figsize=(8, 6))

    if faiss_y:
        plt.plot(k_values, faiss_y, marker="o", color="crimson", linestyle="-", linewidth=2, label="FAISS PQ (m=dim/2, 8-bit)")
    if tq_y:
        plt.plot(k_values, tq_y, marker="s", color="royalblue", linestyle="--", linewidth=2, label="TurboQuant (4-bit)")

    plt.xlabel("k (Top-k Candidates)", fontsize=12)
    plt.ylabel("Recall-1-@k (Accuracy)", fontsize=12)
    plt.title(f"Recall-1-@k Accuracy Comparison (OpenAI d={dim})", fontsize=14, fontweight="bold")
    plt.xscale("log", base=2)
    plt.xticks(k_values, labels=[str(k) for k in k_values])
    plt.ylim([0.0, 1.05])
    plt.grid(True, which="both", linestyle="--", alpha=0.5)
    plt.legend(fontsize=11, loc="lower right")

    png_path = os.path.join(RESULTS_DIR, f"recall_chart_d{dim}_4bit.png")
    plt.savefig(png_path, dpi=300, bbox_inches="tight")
    plt.close()
    print(f"✓ Successfully generated recall chart: {png_path}")

def main():
    os.makedirs(RESULTS_DIR, exist_ok=True)
    plot_dimension(1536)
    plot_dimension(3072)

if __name__ == "__main__":
    main()
