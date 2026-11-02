# Deep reinforcement learning for physical robots

Deep RL can teach a robot a feedback policy from experience, but collecting
that experience on hardware is slow, costly, and sometimes dangerous. A
successful robotics project therefore treats the learned policy as one
component in a controlled system: train mostly in simulation or from
demonstrations, transfer deliberately, and keep independent limits between
the policy and the actuators.

This guide is a practical companion to the [deep RL reference](README.md),
which explains the algorithms and their learning updates. It focuses on
turning a task into a training problem, choosing an algorithm, moving from
simulation to a real robot, and evaluating the result. The examples use
generic mobile-manipulator and arm terminology; adapt limits, rates, and
interfaces to the specific robot and its manufacturer documentation.

## 1. Where RL fits in a robot control stack

A deployed policy should usually choose bounded setpoints or motion
increments, not bypass the robot's existing low-level control and safety
systems. A common stack is:

```text
sensors → state estimator → policy → safety filter → motion controller → actuators
             ↑                     │                         │
             └──── observations ───┴──── measured state ────┘
```

The policy might run at a moderate task-control rate and request a target
pose, velocity, gripper position, or short-horizon trajectory. A tested
joint/velocity/torque controller handles the faster actuator loop. The
robot-specific controller, emergency stop, watchdog, and hardware limits
remain authoritative; the policy cannot override them.

This separation has three benefits: a delayed policy inference does not
leave actuators with an unbounded command, the low-level controller absorbs
small timing and tracking errors, and the learned component can be tested
against a stable command interface. Direct torque policies can be useful
research tools, but they demand tighter timing, stronger safety measures,
and a more accurate simulator than setpoint-level policies.

## 2. Turn the task into an RL problem

Before selecting PPO or SAC, specify the interaction contract. A useful
task definition answers these questions:

| Part | Robotics example | Design question |
|---|---|---|
| Observation `o_t` | joint positions/velocities, end-effector pose, object pose, contact, goal | What can be measured reliably at decision time? |
| Action `a_t` | bounded joint-velocity or Cartesian delta setpoint | What command can the controller execute safely? |
| Reward `r_t` | progress, task completion, penalties for collisions or excessive effort | Does maximizing the score produce the intended behavior? |
| Episode | reset to a known object and robot configuration | Can success, failure, and timeout be distinguished? |
| Constraints | joint limits, workspace, force, speed, collision limits | Which constraints must be guaranteed outside the reward? |

### Observations

Start with compact, calibrated state rather than raw camera pixels if the
task permits it. For a reaching arm, a state vector might contain joint
positions and velocities, the target pose in the robot base frame, and a
gripper state. Include velocity or a short history when position alone
cannot reveal motion. Include timestamps and validity information for
delayed or missing sensors; stale camera frames are not current state.

Use consistent units and frames (for example, radians, metres, and a named
robot base frame). Normalize features with statistics computed from
training data, then freeze and deploy those exact statistics with the
policy. Do not normalize each observation independently: that can erase
meaningful absolute scale and geometry.

### Actions

Choose the narrowest useful action space. An action such as a small
Cartesian pose delta or a short velocity setpoint is often easier to
bound than an unrestricted joint-torque vector. Define explicit minimum
and maximum values, then clamp and rate-limit commands at the execution
boundary as well as in training. Record whether the policy output is a
position, velocity, torque, or delta; confusing these semantics can turn a
small normalized number into a large physical command.

For continuous actions, a squashed Gaussian policy (commonly used by SAC
and PPO) maps normalized outputs into finite bounds. Store those bounds
and any action scaling in the deployment artifact. Avoid adding ad-hoc
noise to the deployed policy: exploration noise belongs in training, not
in a task-running robot.

### Rewards and termination

Use a measurable task objective, and make completion explicit. For a
reaching task, a shaped reward can combine distance-to-target progress,
success, and effort:

```text
r_t = w_progress · (d_{t-1} − d_t)
    + w_success · 1[goal reached]
    − w_effort · ||a_t||²
    − w_collision · 1[unsafe contact]
```

This is only a starting point. Check the units and relative scales, inspect
the resulting behavior, and test whether the policy can increase reward
without completing the task. A large progress reward can encourage
oscillation; a poorly designed contact penalty can prevent useful contact;
a success bonus can be exploited if success detection is wrong. Keep hard
limits as controller constraints, not just negative reward terms.

Represent success, physical failure, and time-limit truncation separately.
A timeout ends data collection but is not necessarily a terminal physical
state; value targets should bootstrap through truncation where appropriate.
This distinction is covered in the [DQN section](README.md#4-deep-q-networks-dqn-in-depth-with-pytorch).

## 3. Choose an algorithm that matches the robot and data budget

Physical robots generally have continuous actions and few affordable
interactions. Begin with a simple baseline and use simulation, logs, or
demonstrations to buy data efficiency before increasing model complexity.

| Method | Best fit | Robotics tradeoff |
|---|---|---|
| Behaviour cloning (BC) | Demonstrations from a person or scripted controller | Fast and simple baseline; errors compound in states absent from demonstrations |
| DAgger | BC policy can be queried by an expert during learner rollouts | Corrects distribution shift; requires safe learner rollouts and an available expert |
| PPO | Many parallel simulation environments; robust first on-policy baseline | Straightforward and stable to implement; real transitions are discarded after updates |
| SAC | Continuous actions and a simulator or safe replay collection | Off-policy replay reuses data and entropy aids exploration; critic errors and reward scaling need attention |
| TD3 | Continuous deterministic control with adequate replay | Efficient and often strong; exploration noise and critic overestimation need tuning |
| Offline RL (for example, IQL or CQL-style methods) | A fixed dataset from teleoperation or prior controllers | No exploratory hardware rollout required for training; distribution shift makes extrapolated actions risky |
| Model-based RL / MPC | Useful dynamics can be learned or calibrated and planning latency is acceptable | Can reduce real samples; model error grows over long imagined rollouts |

Practical starting points:

- **Discrete choices** (select a grasp primitive, choose a navigation mode):
  DQN or PPO, depending on whether replay reuse or a simpler on-policy
  setup matters more.
- **Continuous setpoints in simulation**: SAC is a useful default; compare
  against PPO or TD3 on the same observation, reward, and evaluation
  protocol.
- **Limited real-robot interaction**: clone demonstrations first, then
  consider offline RL or conservative fine-tuning in simulation. Prefer
  small, explicitly budgeted on-robot updates over unconstrained
  exploration.
- **Known, repeatable dynamics and strict constraints**: a conventional
  controller or model-predictive controller may be a better fit than
  learning the entire control law. RL can still choose goals, modes, or
  parameters at a higher level.

The detailed derivations and implementation patterns are in the main
[algorithm guide](README.md): [DQN](README.md#4-deep-q-networks-dqn-in-depth-with-pytorch),
[REINFORCE](README.md#5-policy-gradients-reinforce),
[PPO](README.md#6-actor-critic-and-ppo),
[SAC and TD3](README.md#7-continuous-control-ddpg-td3-sac),
[algorithm selection](README.md#13-choosing-an-algorithm), and
[imitation learning](README.md#17-learning-from-experts-and-people-behaviour-cloning-dagger-gail-dpo-grpo).

## 4. Train in simulation, then measure the gap

Simulation makes resets cheap, permits parallel rollouts, and allows
failures without damaging hardware. It does not make the real robot
identical to the model. Transfer quality depends on matching the parts of
the environment the policy uses: command delay, actuator response,
friction, mass and centre of gravity, contact, sensor noise, camera
calibration, and state-estimation error.

A practical sim-to-real loop is:

1. **Calibrate first.** Measure the robot's motion and sensor delay under
   low-risk commands; fit controller gains and basic dynamics where
   possible. A simulator with wildly inaccurate parameters is not made
   useful by randomizing everything broadly.
2. **Randomize measured uncertainty.** During training, vary plausible
   masses, friction, payloads, actuator gains, observation noise, and
   latency around measured ranges. Expand ranges only when hardware tests
   show a real mismatch. This is domain randomization: robustify against a
   distribution of plausible robots rather than one ideal model.
3. **Randomize task conditions.** Vary object poses, goals, lighting or
   camera viewpoints where they matter. Keep training and evaluation
   conditions distinct so reported performance is not just memorization
   of reset seeds.
4. **Use demonstrations or system identification.** Seed exploration with
   teleoperation, scripted trajectories, or a conventional controller.
   Fit residual dynamics from safe logs when the simulator's errors are
   systematic.
5. **Evaluate robustness, not only average reward.** Include held-out
   dynamics and object placements; inspect worst-case failures and
   completion rates. A high mean return can hide a small number of
   damaging failures.

An alternative to transferring a complete policy is **residual learning**:
keep a trusted nominal controller and let a learned policy add a bounded
correction. This narrows what the network must learn, but the residual is
still subject to limits and independent validation.

## 5. Collect and use real-robot data deliberately

Real rollouts cost wall-clock time and create wear. Plan the data protocol
before training:

- Use teleoperation or a scripted baseline to collect successful and
  recovery examples. Log observations, actions, timestamps, rewards,
  terminal reasons, controller state, and safety interventions together.
- Keep training and evaluation logs separate. Include failed attempts;
  filtering them all out can produce a dataset that teaches no recovery
  behavior.
- Synchronize sensor and action timestamps. A transition paired with the
  wrong camera frame or delayed state estimate teaches incorrect cause
  and effect.
- Store the policy version, robot configuration, software version,
  calibration, normalizer, and random seed with each run. Never combine
  transitions from incompatible action semantics without converting them.
- Prefer offline learning or simulation fine-tuning when an exploratory
  action could collide, drop an object, or exceed a physical limit.

When adapting on hardware, make each experiment reversible: a small
update budget, a known-good checkpoint, a scripted fallback, and a
predefined stop condition. Log safety-filter interventions. Frequent
interventions are evidence the learned policy is out of distribution, not
a reason to silently increase the filter's limits.

## 6. Deployment architecture and safety boundaries

Keep policy inference behind a narrow interface. A ROS 2 deployment might
subscribe to a synchronized observation topic and publish bounded
setpoints to a controller action or command topic; the exact interface
depends on the driver and controller. The important properties are
independent of middleware:

```text
read latest valid observation
    → check age, units, and finite values
    → normalize with the training statistics
    → run policy with a bounded compute deadline
    → unnormalize action and clamp to configured limits
    → check workspace / joint / velocity constraints
    → send to tested low-level controller
    → log command, observation, latency, and interventions
```

At every command boundary, define behavior for stale sensors, invalid
values, inference overruns, communication loss, controller rejection, and
process failure. The safe response is robot-specific: stop, hold a safe
position, or hand control to a validated fallback. Use the platform's
emergency stop and watchdog; a Python exception handler is not a safety
system.

The policy artifact should bundle its network weights, observation order
and units, normalization statistics, action bounds and scaling, model
version, and compatible robot/controller configuration. Verify at startup
that the artifact and robot interface agree. Run inference deterministically
for evaluation unless the application deliberately requires stochastic
actions; a stochastic training policy is commonly evaluated using its
mean action or a fixed sampling configuration.

## 7. A staged validation plan

Only proceed when the current stage meets predeclared task and safety
criteria. Hardware tests should be supervised and use the robot's approved
operating procedures.

| Stage | Test | Evidence to keep |
|---|---|---|
| 1. Offline checks | Tensor shapes, units, normalization, finite outputs, action bounds | Unit tests and a versioned policy artifact |
| 2. Replay / visualization | Run recorded observations through the policy; inspect commanded trajectories | Videos, distributions, boundary-condition checks |
| 3. Simulation evaluation | Held-out seeds and dynamics; compare against scripted and imitation baselines | Success rate, return, failure categories, latency |
| 4. Hardware in a constrained setup | Reduced speed/force, restricted workspace, supervised short trials | Interventions, tracking error, near misses, recovery behavior |
| 5. Expanded operation | Increase task and operating envelope only after criteria pass | Success and failure rates by condition, rollback checkpoint |

Report success rate with a confidence interval, not only the best episode
or average reward. Also report collisions or safety stops, completion
time, intervention rate, hardware conditions, and the number of trials.
Compare with a conventional controller and a demonstration baseline.
For stochastic algorithms, train across multiple seeds and evaluate frozen
policies separately from training.

## 8. Example: learning a tabletop reach-and-grasp behavior

1. **Baseline:** teleoperate the arm or use a scripted grasp sequence;
   verify that the low-level controller can reach the workspace reliably.
2. **Task contract:** observe robot joint state, end-effector pose, object
   pose, and gripper state; command bounded Cartesian increments plus a
   gripper setpoint; define success from an independent grasp/lift
   condition.
3. **Dataset:** record demonstrations with synchronized observations,
   action commands, and success/failure labels. Train a behaviour-cloning
   policy and measure success on held-out object placements.
4. **Simulation:** reproduce the action interface and controller, then
   randomize object pose, grasp friction, payload, sensor noise, and
   measured latency. Train SAC or PPO and compare against the cloned
   policy and scripted baseline.
5. **Transfer:** first replay the policy on recorded observations; then
   run supervised, low-speed trials in a restricted workspace. Start with
   familiar objects and placements, and preserve a known-good fallback.
6. **Improve:** categorize failures (perception, reach, contact, grasp,
   lift, timing). Add demonstrations or improve the model for that failure
   mode; do not respond to every failure by changing the reward or
   algorithm.

The algorithm is a relatively small part of this workflow. Reliable
state estimation, aligned data, a useful simulator, well-chosen action
semantics, and honest hardware evaluation usually determine whether the
policy transfers.

## Further reading and tools

- [Deep RL algorithms and PyTorch patterns](README.md)
- [Gymnasium Robotics environments](https://robotics.farama.org/) — task
  interfaces and simulation benchmarks
- [MuJoCo](https://mujoco.org/) — physics simulation
- [Isaac Lab](https://isaac-sim.github.io/IsaacLab/) — GPU-accelerated
  robot-learning simulation
- [ROS 2 control](https://control.ros.org/) — controller and hardware
  integration concepts
- [Stable-Baselines3](https://stable-baselines3.readthedocs.io/) — common
  PyTorch implementations of PPO, SAC, TD3, and DQN
