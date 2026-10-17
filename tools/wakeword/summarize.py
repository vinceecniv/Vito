"""One line per operating point of an evaluate.py report: for each threshold,
recall (clean / meeting babble) against false accepts per hour.

    python summarize.py report.json [report.json ...]
"""
import json
import sys


def main():
    for path in sys.argv[1:]:
        r = json.load(open(path))
        fa, rc = r["false_accepts_per_hour"], r["recall"]
        clean = next(v for k, v in rc.items() if "clean" in k)["all"]
        b10 = next(v for k, v in rc.items() if "babble10" in k)["all"]
        conf = list(r["confusables"].values())[0]
        print(path)
        print("   t   clean babble10 | FA/h nl   ami   val | confusable")
        for t in clean:
            print(f"  {float(t):.2f}  {clean[t]:.2f}  {b10[t]:.2f}    | {fa['fleurs_nl.npy'][t]:5.2f} {fa['ami.npy'][t]:5.2f} "
                  f"{fa['validation_set_features.npy'][t]:5.2f} | {conf[t]:.2f}")


if __name__ == "__main__":
    main()
