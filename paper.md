4
2
0
2

r
p
A
1
1

]
I

A
.
s
c
[

4
v
7
8
9
2
1
.
1
0
3
2
:
v
i
X
r
a

The Optimal Choice of Hypothesis
Is the Weakest, Not the Shortest

Michael Timothy Bennett1
[0000−0001−6895−8782]

The Australian National University
michael.bennett@anu.edu.au
http://www.michaeltimothybennett.com/

Abstract. If A and B are sets such that A ⊂ B, generalisation may
be understood as the inference from A of a hypothesis suﬃcient to con-
struct B. One might infer any number of hypotheses from A, yet only
some of those may generalise to B. How can one know which are likely
to generalise? One strategy is to choose the shortest, equating the ability
to compress information with the ability to generalise (a “proxy for intel-
ligence”). We examine this in the context of a mathematical formalism
of enactive cognition. We show that compression is neither necessary nor
suﬃcient to maximise performance (measured in terms of the probability
of a hypothesis generalising). We formulate a proxy unrelated to length
or simplicity, called weakness. We show that if tasks are uniformly dis-
tributed, then there is no choice of proxy that performs at least as well
as weakness maximisation in all tasks while performing strictly better
in at least one. In experiments comparing maximum weakness and min-
imum description length in the context of binary arithmetic, the former
generalised at between 1.1 and 5 times the rate of the latter. We argue
this demonstrates that weakness is a far better proxy, and explains why
Deepmind’s Apperception Engine is able to generalise eﬀectively1.

Keywords: simplicity · induction · artiﬁcial general intelligence.

1 Introduction

If A and B are sets such that A ⊂ B, generalisation may be understood as
the inference from A of a hypothesis suﬃcient to construct B. One might infer
any number of hypotheses from A, yet only some of those may generalise to B.
How can one know which are likely to generalise? According to Ockham’s Razor,
the simpler of two explanations is the more likely [2]. Simplicity is not itself a
measurable property, so the minimum description length principle [3] relates sim-
plicity to length. Shorter representations are considered to be simpler, and tend
to generalise more eﬀectively. This is often applied in the context of induction
by comparing the length of programs that explain what is observed (to chose
the shortest, all else being equal). The ability to identify shorter representations

1 Appendices are to be found on GitHub [1].

2

Michael Timothy Bennett

is compression, and the ability to generalise is arguably intelligence [4]. Hence
the ability to compress information is often portrayed as a proxy for intelligence
[5], even serving as the foundation [6, 7, 8] of the theoretical super-intelligence
AIXI [9]. That compression is a good proxy seems to have gone unchallenged.
The optimal choice of hypothesis is widely considered to be the shortest. We
show that it is not2. We present an alternative, unrelated to description length,
called weakness. We prove that to maximise the probability that one’s hypothe-
ses generalise, it is necessary and suﬃcient to infer the weakest valid hypotheses
possible3.

2 Background deﬁnitions

To do so, we employ a formalism of enactive cognition [10, 11, 12, 13, 14, 1],
in which sets of declarative programs are related to one another in such a way
as to form a lattice. This unusual representation is necessary to ensure that
both the weakness and description length of a hypothesis are well deﬁned4. This
formalism can be understood in three steps.

1. The environment is represented as a set of declarative programs.
2. A ﬁnite subset of the environment is used to deﬁne a language with which

to write statements that behave as logical formulae.

3. Finally, induction is formalised in terms of tasks made up of these statements.

Deﬁnition 1 (environment).

– We assume a set Φ whose elements we call states, one of which we single

out as the present state5.

– A declarative program is a function f : Φ → {true, f alse}, and we write
P for the set of all declarative programs. By an objective truth about a
state φ, we mean a declarative program f such that f (φ) = true.

Deﬁnition 2 (implementable language).

– V = {V ⊂ P : V is f inite} is a set whose elements we call vocabular-
ies, one of which we single out as the vocabulary v for an implementable
language.

2 This proof is conditional upon certain assumptions regarding the nature of cognition

as enactive, and a formalism thereof.

3 Assuming tasks are uniformly distributed, and weakness is well deﬁned.
4 An example of how one might translate propositional logic into this representation
is given at the end of this paper. It is worth noting that this representation of
logical formulae addresses the symbol grounding problem [15], and was speciﬁcally
constructed to address subjective performance claims in the context of AIXI [16].
5 Each state is just reality from the perspective of a point along one or more dimen-
sions. States of reality must be separated by something, or there would be only one
state of reality. For example two diﬀerent states of reality may be reality from the
perspective of two diﬀerent points in time, or in space and so on.

The Optimal Choice of Hypothesis Is the Weakest, Not the Shortest

3

– Lv = {l ⊆ v : ∃φ ∈ Φ (∀p ∈ l : p(φ) = true)} is a set whose elements we
call statements6. Lv follows from Φ and v. We call Lv an implementable
language.

– l ∈ Lv is true iﬀ the present state is φ and ∀p ∈ l : p(φ) = true.
– The extension of a statement a ∈ Lv is Za = {b ∈ Lv : a ⊆ b}.
– The extension of a set of statements A ⊆ Lv is ZA = S
Za.
a∈A

(Notation) Z with a subscript is the extension of the subscript7. Lower case
letters represent statements, and upper case represent sets of statements.

Deﬁnition 3 (v-task). For a chosen v, a task α is hSα, Dα, Mαi where:

– Sα ⊂ Lv is a set whose elements we call situations of α.
– Sα has the extension ZSα, whose elements we call decisions of α.
– Dα = {z ∈ ZSα : z is correct} is the set of all decisions which complete α.
– Mα = {l ∈ Lv : ZSα ∩ Zl = Dα} whose elements we call models of α.
Γv is the set of all tasks8.

(Notation) If ω ∈ Γv, then we will use subscript ω to signify parts of ω, meaning
one should assume ω = hSω, Dω, Mωi even if that isn’t written.

(How a task is completed) Assume we’ve a v-task ω and a hypothesis h ∈ Lv s.t.

1. we are presented with a situation s ∈ Sω, and
2. we must select a decision z ∈ Zs ∩ Zh.
3. If z ∈ Dω, then z is correct and the task is complete. This occurs if h ∈ Mω.

3 Formalising induction

Deﬁnition 4 (probability). We assume a uniform distribution over Γv.

Deﬁnition 5 (generalisation). A statement l generalises to α ∈ Γv iﬀ l ∈ Mα.
We say l generalises from α to v-task ω if we ﬁrst obtain l from Mα and then
ﬁnd it generalises to ω.

Deﬁnition 6 (child and parent). A v-task α is a child of v-task ω if Sα ⊂ Sω
and Dα ⊆ Dω. This is written as α ⊏ ω. If α ⊏ ω then ω is then a parent of α.

A proxy is meant to estimate one thing by measuring another. In this case,
if intelligence is the ability to generalise [10, 4], then a greater proxy value is
meant to indicate that a statement is more likely to generalise. Not all proxies
are eﬀective (most will be useless). We focus on two in particular.

6 Statements are the logical formulae about which we will reason.
7 e.g. Zs is the extension of s.
8 For example, we might represent chess as a supervised learning problem where s ∈ Sα
is the state of a chessboard, z ∈ Zs is a sequence of moves by two players that begins
in s, and d ∈ Dα ∩ Zs is such a sequence of moves that terminates in victory for one
player in particular (the one undertaking the task).

4

Michael Timothy Bennett

Deﬁnition 7 (proxy for intelligence). A proxy is a function parameterized
by a choice of v such that qv : Lv → N. The set of all proxies is Q.

(Weakness) The weakness of a statement l is the cardinality of its extension |Zl|.
There exists qv ∈ Q such that qv(l) = |Zl|.

(Description length) The description length of a statement l is its cardinality |l|.
Longer logical formulae are considered less likely to generalise [3], and a proxy
is something to be maximised, so description length as a proxy is qv ∈ Q such
that qv(l) = 1
|l| .

A child task may serve as an ostensive deﬁnition [17] of its parent, meaning

one can generalise from child to parent.

Deﬁnition 8 (induction). α and ω are v-tasks such that α ⊏ ω. Assume we
are given a proxy qv ∈ Q, the complete deﬁnition of α and the knowledge that
α ⊏ ω. We are not given the deﬁnition of ω. The process of induction would
proceed as follows:

1. Obtain a hypothesis by computing a model h ∈ arg max
m∈Mα

2. If h ∈ Mω, then we have generalised from α to ω.

qv(m).

4 Proofs

Proposition 1 (suﬃciency). Weakness is a proxy suﬃcient to maximise the
probability that induction generalises from α to ω.

Proof: You’re given the deﬁnition of v-task α from which you infer a hypothesis
h ∈ Mα. v-task ω is a parent of α to which we wish to generalise:

1. The set of statements which might be decisions addressing situations in Sω

and not Sα, is ZSα = {l ∈ Lv : l /∈ ZSα}.

2. For any given h ∈ Mα, the extension Zh of h is the set of decisions h
implies. The subset of Zh which fall outside the scope of what is required
for the known task α is ZSα ∩ Zh (because ZSα is the set of all decisions we
might make when attempting α, and so the set of all decisions that can’t be
made when undertaking α is ZSα because those decisions occur in situations
that aren’t part of Sα).

3. |ZSα ∩ Zh| increases monotonically with |Zh|, because ∀z ∈ Zm : z /∈ ZSα →

z ∈ ZSα.

4. 2|ZSα | is the number of tasks which fall outside of what it is necessary for a
model of α to generalise to (this is just the powerset of ZSα deﬁned in step
2), and 2|ZSα ∩Zh| is the number of those tasks to which a given h ∈ Mα does
generalise.

The Optimal Choice of Hypothesis Is the Weakest, Not the Shortest

5

5. Therefore the probability that a given model h ∈ Mα generalises to the

unknown parent task ω is

p(h ∈ Mω | h ∈ Mα, α ⊏ ω) =

2|ZSα ∩Zh|
2|ZSα |

p(h ∈ Mω | h ∈ Mα, α ⊏ ω) is maximised when |Zh| is maximised.

Proposition 2 (necessity). To maximise the probability that induction gen-
eralises from α to ω, it is necessary to use weakness as a proxy, or a function
thereof9.

Proof: Let α and ω be deﬁned exactly as they were in the proof of prop. 1.
1. If h ∈ Mα and ZSω ∩ Zh = Dω, then it must be he case that Dω ⊆ Zh.
2. If |Zh| < |Dω| then generalisation cannot occur, because that would mean

that Dω 6⊆ Zh.

3. Therefore generalisation is only possible if |Zm| ≥ |Dω|, meaning a suﬃ-
ciently weak hypothesis is necessary to generalise from child to parent.
4. The probability that |Zm| ≥ |Dω| is maximised when |Zm| is maximised.
Therefore to maximise the probability induction results in generalisation, it
is necessary to select the weakest hypothesis.

To select the weakest hypothesis, it is necessary to use weakness (or a function
thereof) as a proxy.

Remark 1 (prior). The above describes inference from a child to a parent. How-
ever, it follows that increasing the weakness of a statement increases the proba-
bility that it will generalise to any task (not just a parent of some given child). As
tasks are uniformly distributed, every statement in Lv is a model to one or more
tasks, and the number of tasks to which each statement l ∈ Lv generalises is 2|Zl|.
Hence the probability of generalisation10 to ω is p(h ∈ Mω | h ∈ Lv) = 2|Zh |
2|Lv | .
This assigns a probability to every statement l ∈ Lv given an implementable
language. It is a probability distribution in the sense that the probability of mu-
tually exclusive statements sums to one11. This prior may be considered universal
in the very limited sense that it assigns a probability to every conceivable hy-
pothesis (where what is conceivable depends upon the implementable language)
absent any parameters or speciﬁc assumptions about the task as with AIXI’s
intelligence order relation [9, def. 5.14 pp. 147]12. As the vocabulary v is ﬁnite,
Lv must also be ﬁnite, and so p is computable.
9 For example we might use weakness multiplied by a constant to the same eﬀect.
10 2|Zh |

2|Lv | is maximised when h = ∅, because the optimal hypothesis given no information
is to assume nothing (you’ve no sequence to predict, so why make assertions that
might contradict the environment?).

11 Two statements a and b are mutually exclusive if a 6∈ Zb and b 6∈ Za, which we’ll write
as µ(a, b). Given x ∈ Lv, the set of all mutually exclusive statements is a set Kx ⊂ Lv
such that x ∈ Kx and ∀a, b ∈ Kx : µ(a, b). It follows that ∀x ∈ Lv, P
b∈Kx

p(b) = 1.
12 We acknowledge that some may object to the term universal, because v is ﬁnite.

6

Michael Timothy Bennett

We have shown that, if tasks are uniformly distributed, then weakness is a neces-
sary and suﬃcient proxy to maximise the probability that induction generalises.
It is important to note that another proxy may perform better given cherry-
picked combinations of child and parent task for which that proxy is suitable.
However, such a proxy would necessarily perform worse given the uniform dis-
tribution of all tasks. Can the same be said of description length?

Proposition 3. Description length is neither a necessary nor suﬃcient proxy
for the purposes of maximising the probability that induction generalises.

Proof: In propositions 1 and 2 we proved that weakness is a necessary and suﬃ-
cient choice of proxy to maximise the probability of generalisation. It follows that
either maximising 1
|m| (minimising description length) maximises |Zm| (weak-
ness), or minimisation of description length is unnecessary to maximise the prob-
ability of generalisation. Assume the former, and we’ll construct a counterexam-
ple with v = {a, b, c, d, e, f, g, h, j, k, z} s.t. Lv = {{a, b, c, d, j, k, z}, {e, b, c, d, k},
{a, f, c, d, j}, {e, b, g, d, j, k, z}, {a, f, c, h, j, k}, {e, f, g, h, j, k}} and a task α where

– Sα = {{a, b}, {e, b}}
– Dα = {{a, b, c, d, j, k, z}, {e, b, g, d, j, k, z}}
– Mα = {{z}, {j, k}}

Weakness as a proxy selects {j, k}, while description length as a proxy selects
{z}. This demonstrates the minimising description length does not necessarily
maximise weakness, and maximising weakness does not minimise description
length. As weakness is necessary and suﬃcient to maximise the probability of
generalisation, it follows that minimising description length is neither.

5 Experiments

Included with this paper is a Python script to perform two experiments using
PyTorch with CUDA, SymPy and A∗ [18, 19, 20, 21] (see technical appendix
for details). In these two experiments, a toy program computes models to 8-
bit string prediction tasks (binary addition and multiplication). The purpose of
these experiments was to compare weakness and description length as proxies.

5.1 Setup

To specify tasks with which the experiments would be conducted, we needed
a vocabulary v with which to describe simple 8-bit string prediction problems.
There were 256 states in Φ, one for every possible 8-bit string. The possible
statements were then all the expressions regarding those 8 bits that could be
written in propositional logic (the simple connectives ¬, ∧ and ∨ needed to
perform binary arithmetic – a written example of how propositional logic can be
used in to specify v is also included in the appendix). In other words, for each
statement in Lv there existed an equivalent expression in propositional logic.

The Optimal Choice of Hypothesis Is the Weakest, Not the Shortest

7

For eﬃciency, these statements were implemented as either PyTorch tensors or
SymPy expressions in diﬀerent parts of the program, and converted back and
forth as needed (basic set and logical operations on these propositional tensor
representations were implemented for the same reason). A v-task was speciﬁed
by choosing Dn ⊂ Lv such that all d ∈ Dn conformed to the rules of either binary
addition or multiplication with 4-bits of input, followed by 4-bits of output.

5.2 Trials

Each experiment had parameters were “operation” and “number_of_trials”. For
each trial the number |Dk| of examples ranged from 4 to 14. A trial had 2 phases.

Training phase:

1. A task n (referred to in code as Tn) was generated:

(a) First, every possible 4-bit input for the chosen binary operation was used

to generate an 8-bit string. These 16 strings then formed Dn.

(b) A bit between 0 and 7 was then chosen, and Sn created by cloning Dn
and deleting the chosen bit from every string (Sn contained 16 diﬀerent
7-bit strings, each of which was a sub-string of an element of Dn).
2. A child-task k = hSk, Dk, Mki (referred to in code as Tk) was sampled (as-
suming a uniform distribution over children) from the parent task Tn. Recall,
|Dk| was determined as a parameter of the trial.

3. From Tk two models were then generated; a weakest cw, and a MDL cmdl.

Testing phase: For each model c ∈ {cw, cmdl}, the testing phase was as follows:

1. The extension Zc of c was then generated.
2. A prediction Drecon was made s.t. Drecon = {z ∈ Zc : ∃s ∈ Sn (s ⊂ z)}.
3. Drecon was then compared to the ground truth Dn, and results recorded.

Between 75 and 256 trials were run for each value of the parameter |Dk|. Fewer
trials were run for larger values of |Dk| as these took longer to process. The
results of these trails were then averaged for each value of |Dk|.

5.3 Results

Two sorts of measurements were taken for each trial. The ﬁrst was the rate at
generalisation occurred. Generalisation was deemed to have occurred where
Drecon = Dn. The number of trials in which generalisation occurred was mea-
sured, and divided by n to obtain the rate of generalisation for cw and cmdl.
Error was computed as a Wald 95% conﬁdence interval. The second measure-
ment was the average extent to which models generalised. Even where
Drecon 6= Dn, the extent to which models generalised could be ascertained.
|Drecon∩Dn|
was measured and averaged for each value of |Dk|, and the standard
|Dn|
error computed. The results (see tables 1 and 2) demonstrate that weakness is a
better proxy for intelligence than description length. The generalisation rate for
cw was between 110 − 500% of cmdl, and the extent was between 103 − 156%.

8

Michael Timothy Bennett

Table 1. Results for Binary Addition

cw

cmdl

|Dk| Rate ±95% AvgExt StdErr Rate ±95% AvgExt StdErr

6
10
14

.11
.27
.68

.039
.064
.106

.75
.91
.98

.008
.006
.005

.10
.13
.24

.037
.048
.097

.48
.69
.91

.012
.009
.006

Table 2. Results for Binary Multiplication

cw

cmdl

|Dk| Rate ±95% AvgExt StdErr Rate ±95% AvgExt StdErr

6
10
14

.05
.16
.46

.026
.045
.061

.74
.86
.96

.009
.006
.003

.01
.08
.21

.011
.034
.050

.58
.78
.93

.011
.008
.003

6 Concluding remarks

We have shown that, if tasks are uniformly distributed, then weakness maximi-
sation is necessary and suﬃcient to maximise the probability that induction will
produce a hypothesis that generalises. It follows that there is no choice of proxy
that performs at least as well as weakness maximisation across all possible com-
binations of child and parent task while performing strictly better in at least one.
We’ve also shown that the minimisation of description length is neither neces-
sary nor suﬃcient. This calls into question the relationship between compression
and intelligence [5, 22, 23], at least in the context of enactive cognition. This is
supported by our experimental results, which demonstrate that weakness is a far
better predictor of whether a hypothesis will generalise, than description length.
Weakness should not be conﬂated with Ockham’s Razor. A simple statement
need not be weak, for example “all things are blue crabs”. Likewise, a complex
utterance can assert nothing. Weakness is a consequence of extension, not form.
If weakness is to be understood as an epistemological razor, it is this (which we
humbly suggest naming “Bennett’s Razor”):

Explanations should be no more speciﬁc than necessary.13

The Apperception Engine: The Apperception Engine [24, 25, 26] (Evans et.
al. of Deepmind) is an inference engine that generates hypotheses that generalise
often. To achieve this, Evans formalised Kant’s philosophy to give the engine a

13 We do not know which possibilities will eventuate. A less speciﬁc statement contra-
dicts fewer possibilities. Of all hypotheses suﬃcient to explain what we perceive, the
least speciﬁc is most likely.

The Optimal Choice of Hypothesis Is the Weakest, Not the Shortest

9

“strong inductive bias”. The engine forms hypotheses from only very general
assertions, meaning logical formulae which are universally quantiﬁed. That is
possible because the engine uses language speciﬁcally tailored to eﬃciently rep-
resent the sort of sequences to which it is applied. Our results suggest a simpler
and more general explanation of why the engine’s hypotheses generalise so well.
The tailoring of logical formulae to represent certain sequences amounts to a
choice of v, and the use of only universally quantiﬁed logical formulae ensures
the resulting hypothesis is weak. Obviously this can work well, but only for the
subset of possible tasks that the vocabulary is able to describe in this way (any-
thing else will not be able to be represented as a universally quantiﬁed rule, and
so will not be represented at all [27]). This illustrates how future research may
explore choices of v in aid of more eﬃcient induction in particular sorts of task,
such as the inference of linguistic meaning and intent (see appendix).

Neural networks: How might a task be represented in the context of a func-
tion? Though we use continuous real values in base 10 to formalise neural net-
works, all computation still takes place in a discrete, ﬁnite and binary system. A
ﬁnite number of imperative programs composed a ﬁnite number of times may be
represented by a ﬁnite set of declarative programs. Likewise, activations within
a network given an input can be represented as a ﬁnite set of declarative pro-
grams, expressing a decision. The choice of architecture speciﬁes the vocabulary
in which this is written, determining what sort of relations can be described
according to the Chomsky Hierarchy [28]. The reason why LLMs are so prone to
fabrication and inconsistency may be because they are optimised only to min-
imise loss, rather than maximise weakness [10]. Perhaps grokking [29] can be
induced by optimising for weakness. Future research should investigate means
by which weakness can be maximised in the context of neural networks.

References

[1] M. T. Bennett. Appendices. Version 1.2.1. 2023. doi: 10.5281/zenodo.7641742.

url: github.com/ViscousLemming/Technical-Appendices.

[2] E. Sober. Ockham’s Razors: A User’s Manual. Cambridge Uni. Press, 2015.
[3] J. Rissanen. “Modeling By Shortest Data Description*”. In: Autom. 14

(1978), pp. 465–471.

[4] F. Chollet. On the Measure of Intelligence. 2019.
[5] G. Chaitin. “The Limits of Reason”. In: Sci. Am. 294.3 (2006), pp. 74–81.
[6] R. Solomonoﬀ. “A formal theory of inductive inference. Part I”. In: Infor-

mation and Control 7.1 (1964), pp. 1–22.

[7] R. Solomonoﬀ. “A formal theory of inductive inference. Part II”. In: Infor-

mation and Control 7.2 (1964), pp. 224–254.

[8] A. Kolmogorov. “On tables of random numbers”. In: Sankhya: The Indian

Journal of Statistics A (1963), pp. 369–376.

[9] M. Hutter. Universal Artiﬁcial Intelligence: Sequential Decisions Based on

Algorithmic Probability. Berlin, Heidelberg: Springer-Verlag, 2010.

10

Michael Timothy Bennett

[10] M. T. Bennett. “Symbol Emergence and the Solutions to Any Task”. In:

Artiﬁcial General Intelligence. Cham: Springer, 2022, pp. 30–40.

[11] M. T. Bennett. Computational Dualism and Objective Superintelligence.

2023. url: arxiv.org/abs/2302.00843.

[12] M. T. Bennett. “Emergent Causality and the Foundation of Conscious-
ness”. In: Artiﬁcial General Intelligence. Springer, 2023, pp. 52–61.
[13] M. T. Bennett. “On the Computation of Meaning, Language Models and
Incomprehensible Horrors”. In: Artiﬁcial General Intelligence. Springer,
2023, pp. 32–41.

[14] D. Ward, D. Silverman, and M. Villalobos. “Introduction: The Varieties of

Enactivism”. In: Topoi 36 (Apr. 2017).

[15] S. Harnad. “The symbol grounding problem”. In: Physica D: Nonlinear

Phenomena 42.1 (1990), pp. 335–346.

[16] J. Leike and M. Hutter. “Bad Universal Priors and Notions of Optimality”.
In: Proceedings of The 28th COLT, PMLR (2015), pp. 1244–1259.
[17] A. Gupta. “Deﬁnitions”. In: The Stanford Encyclopedia of Philosophy. Ed.

by E. N. Zalta. Winter 2021. Stanford University, 2021.

[18] A. Paszke et al. “PyTorch: An Imperative Style, High-Performance Deep

Learning Library”. In: NeurIPS. USA: Curran Assoc. Inc., 2019.

[19] D. Kirk. “NVIDIA Cuda Software and Gpu Parallel Computing Architec-

ture”. In: ISMM ’07. Canada: ACM, 2007, pp. 103–104.

[20] A. Meurer et al. “SymPy: Symbolic computing in Python”. In: PeerJ Com-

puter Science 3 (Jan. 2017), e103. doi: 10.7717/peerj-cs.103.

[21] P. E. Hart, N. J. Nilsson, and B. Raphael. “A Formal Basis for the Heuris-
tic Determination of Minimum Cost Paths”. In: IEEE Transactions on
Systems Science and Cybernetics 4.2 (1968), pp. 100–107.

[22] J. Hernández-Orallo and D. L. Dowe. “Measuring universal intelligence:
Towards an anytime intelligence test”. In: Artiﬁcial Intelligence 174.18
(2010), pp. 1508–1539.

[23] S. Legg and J. Veness. “An Approximation of the Universal Intelligence

Measure”. In: Algorithmic Probability and Friends. 2011.

[24] R. Evans. “Kant’s Cognitive Architecture”. PhD thesis. Imperial, 2020.
[25] R. Evans, M. Sergot, and A. Stephenson. “Formalizing Kant’s Rules”. In:

J Philos Logic 49 (2020), pp. 613–680.

[26] R. Evans et al. “Making Sense of Raw Input”. In: Artiﬁcial Intelligence

299 (2021).

[27] M. T. Bennett. “Compression, The Fermi Paradox and Artiﬁcial Super-
Intelligence”. In: Artiﬁcial General Intelligence. Springer, 2022, pp. 41–44.

[28] G. Delétang et al. Neural Networks and the Chomsky Hierarchy. 2022.
[29] A. Power et al. “Grokking: Generalization Beyond Overﬁtting on Small Al-

gorithmic Datasets”. In: ICLR. 2022. url: https://arxiv.org/abs/2201.02177.


